package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

const (
	pmsetInterval    = 30 * time.Second // интервал опроса pmset
	profilerInterval = 2 * time.Minute  // интервал опроса system_profiler
)

// DataCollector управляет оптимизированным сбором данных
type DataCollector struct {
	db               *sqlx.DB
	buffer           *MemoryBuffer
	retention        *DataRetention
	lastProfilerCall time.Time
	pmsetInterval    time.Duration
	profilerInterval time.Duration
}

// MemoryBuffer - буфер в памяти для быстрого доступа к последним измерениям
type MemoryBuffer struct {
	measurements    []Measurement
	maxSize         int
	mu              sync.RWMutex
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

// DataRetention управляет ретенцией данных в БД
type DataRetention struct {
	db              *sqlx.DB
	retentionPeriod time.Duration
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

// DataService - сервис для работы с данными батареи
type DataService struct {
	collector        *DataCollector
	db               *sqlx.DB
	buffer           *MemoryBuffer
	ctx              context.Context
	cancel           context.CancelFunc
	caffeinate       *exec.Cmd
	caffeineActive   bool
}

// NewMemoryBuffer создает новый буфер в памяти
func NewMemoryBuffer(maxSize int) *MemoryBuffer {
	return &MemoryBuffer{
		measurements:    make([]Measurement, 0, maxSize),
		maxSize:         maxSize,
		lastCleanup:     time.Now(),
		cleanupInterval: 24 * time.Hour, // Очистка раз в сутки
	}
}

// Add добавляет новое измерение в буфер
func (mb *MemoryBuffer) Add(m Measurement) {
	mb.mu.Lock()
	defer mb.mu.Unlock()

	// Добавляем новое измерение
	mb.measurements = append(mb.measurements, m)

	// Если превышен максимальный размер, удаляем старые записи
	if len(mb.measurements) > mb.maxSize {
		// Удаляем первую половину старых записей для оптимизации
		keepFrom := len(mb.measurements) - mb.maxSize + mb.maxSize/4
		mb.measurements = mb.measurements[keepFrom:]
	}
}

// GetLast возвращает последние N измерений из буфера
func (mb *MemoryBuffer) GetLast(n int) []Measurement {
	mb.mu.RLock()
	defer mb.mu.RUnlock()

	if len(mb.measurements) == 0 {
		return nil
	}

	if n >= len(mb.measurements) {
		// Возвращаем копию всех измерений
		result := make([]Measurement, len(mb.measurements))
		copy(result, mb.measurements)
		return result
	}

	// Возвращаем копию последних n измерений
	start := len(mb.measurements) - n
	result := make([]Measurement, n)
	copy(result, mb.measurements[start:])
	return result
}

// GetLatest возвращает последнее измерение
func (mb *MemoryBuffer) GetLatest() *Measurement {
	mb.mu.RLock()
	defer mb.mu.RUnlock()

	if len(mb.measurements) == 0 {
		return nil
	}

	// Возвращаем копию последнего измерения
	latest := mb.measurements[len(mb.measurements)-1]
	return &latest
}

// Size возвращает текущий размер буфера
func (mb *MemoryBuffer) Size() int {
	mb.mu.RLock()
	defer mb.mu.RUnlock()
	return len(mb.measurements)
}

// LoadFromDB загружает последние измерения из базы данных в буфер
func (mb *MemoryBuffer) LoadFromDB(db *sqlx.DB, count int) error {
	measurements, err := getLastNMeasurements(db, count)
	if err != nil {
		return fmt.Errorf("загрузка из БД: %w", err)
	}

	mb.mu.Lock()
	defer mb.mu.Unlock()

	mb.measurements = measurements
	return nil
}

// NewDataRetention создает новый менеджер ретенции данных
func NewDataRetention(db *sqlx.DB, retentionPeriod time.Duration) *DataRetention {
	return &DataRetention{
		db:              db,
		retentionPeriod: retentionPeriod,
		lastCleanup:     time.Now(),
		cleanupInterval: 6 * time.Hour, // Проверка каждые 6 часов
	}
}

// Cleanup удаляет старые данные из БД
func (dr *DataRetention) Cleanup() error {
	if time.Since(dr.lastCleanup) < dr.cleanupInterval {
		return nil // Еще рано для очистки
	}

	cutoffTime := time.Now().Add(-dr.retentionPeriod)

	result, err := dr.db.Exec(`
		DELETE FROM measurements 
		WHERE timestamp < ?
	`, cutoffTime.Format(time.RFC3339))

	if err != nil {
		return fmt.Errorf("очистка старых данных: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("🗑️ Удалено %d старых записей (старше %v)", rowsAffected, dr.retentionPeriod)

		// Выполняем VACUUM для освобождения места
		_, err = dr.db.Exec("VACUUM")
		if err != nil {
			log.Printf("⚠️ Ошибка VACUUM: %v", err)
		}
	}

	dr.lastCleanup = time.Now()
	return nil
}

// GetStats возвращает статистику по данным в БД
func (dr *DataRetention) GetStats() (map[string]interface{}, error) {
	var stats map[string]interface{} = make(map[string]interface{})

	// Общее количество записей
	var totalCount int
	err := dr.db.Get(&totalCount, "SELECT COUNT(*) FROM measurements")
	if err != nil {
		return nil, fmt.Errorf("подсчет записей: %w", err)
	}
	stats["total_records"] = totalCount

	// Диапазон дат
	var oldestDate, newestDate string
	err = dr.db.Get(&oldestDate, "SELECT MIN(timestamp) FROM measurements")
	if err == nil {
		stats["oldest_record"] = oldestDate
	}

	err = dr.db.Get(&newestDate, "SELECT MAX(timestamp) FROM measurements")
	if err == nil {
		stats["newest_record"] = newestDate
	}

	// Размер БД файла
	if dbFileInfo, err := os.Stat(getDBPath()); err == nil {
		stats["db_size_mb"] = float64(dbFileInfo.Size()) / (1024 * 1024)
	}

	return stats, nil
}

// NewDataCollector создает новый коллектор данных с буферизацией
func NewDataCollector(db *sqlx.DB) *DataCollector {
	buffer := NewMemoryBuffer(100)                     // Буфер на последние 100 измерений
	retention := NewDataRetention(db, 90*24*time.Hour) // Хранение 3 месяца

	collector := &DataCollector{
		db:               db,
		buffer:           buffer,
		retention:        retention,
		lastProfilerCall: time.Time{},
		pmsetInterval:    30 * time.Second,
		profilerInterval: 2 * time.Minute,
	}

	// Загружаем существующие данные в буфер
	if err := buffer.LoadFromDB(db, 100); err != nil {
		log.Printf("⚠️ Ошибка загрузки данных в буфер: %v", err)
	} else {
		log.Printf("📦 Загружено %d измерений в буфер памяти", buffer.Size())
	}

	return collector
}

// collectAndStore собирает данные и сохраняет их в БД и буфер
func (dc *DataCollector) collectAndStore() error {
	// Получаем базовые данные от pmset
	pct, state, pmErr := parsePMSet()
	if pmErr != nil {
		return fmt.Errorf("сбор данных pmset: %w", pmErr)
	}

	// Создаем базовое измерение
	m := &Measurement{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Percentage:      pct,
		State:           state,
		CycleCount:      0, // Будет обновлено ниже
		FullChargeCap:   0,
		DesignCapacity:  0,
		CurrentCapacity: 0,
		Temperature:     0,
	}

	// Добавляем подробные данные от ioreg, если пора
	if time.Since(dc.lastProfilerCall) >= dc.profilerInterval {
		cycle, fullCap, designCap, currCap, temperature, voltage, amperage, condition, ioErr := parseIORegistry()
		if ioErr == nil {
			m.CycleCount = cycle
			m.FullChargeCap = fullCap
			m.DesignCapacity = designCap
			m.CurrentCapacity = currCap
			m.Temperature = temperature
			m.Voltage = voltage
			m.Amperage = amperage
			m.AppleCondition = condition

			// Вычисляем мощность
			if voltage > 0 && amperage != 0 {
				m.Power = (voltage * amperage) / 1000
			}

			dc.lastProfilerCall = time.Now()
		} else {
			// Если ioreg не работает, используем предыдущие значения
			if latest := dc.buffer.GetLatest(); latest != nil {
				m.CycleCount = latest.CycleCount
				m.FullChargeCap = latest.FullChargeCap
				m.DesignCapacity = latest.DesignCapacity
				m.CurrentCapacity = latest.CurrentCapacity
				m.Temperature = latest.Temperature
				m.Voltage = latest.Voltage
				m.Amperage = latest.Amperage
				m.Power = latest.Power
				m.AppleCondition = latest.AppleCondition
			}
			log.Printf("⚠️ ioreg недоступен, используем кэшированные значения: %v", ioErr)
		}
	} else {
		// Используем последние известные значения
		if latest := dc.buffer.GetLatest(); latest != nil {
			m.CycleCount = latest.CycleCount
			m.FullChargeCap = latest.FullChargeCap
			m.DesignCapacity = latest.DesignCapacity
			m.CurrentCapacity = latest.CurrentCapacity
			m.Temperature = latest.Temperature
			// Копируем также значения напряжения, тока и мощности
			m.Voltage = latest.Voltage
			m.Amperage = latest.Amperage
			m.Power = latest.Power
			m.AppleCondition = latest.AppleCondition
		}
	}

	// Сохраняем в БД
	if err := insertMeasurement(dc.db, m); err != nil {
		return fmt.Errorf("сохранение в БД: %w", err)
	}

	// Добавляем в буфер памяти
	dc.buffer.Add(*m)

	// Периодическая очистка старых данных
	if err := dc.retention.Cleanup(); err != nil {
		log.Printf("⚠️ Ошибка очистки данных: %v", err)
	}

	return nil
}

// GetLatestFromBuffer возвращает последнее измерение из буфера (быстро)
func (dc *DataCollector) GetLatestFromBuffer() *Measurement {
	return dc.buffer.GetLatest()
}

// GetLastNFromBuffer возвращает последние N измерений из буфера (быстро)
func (dc *DataCollector) GetLastNFromBuffer(n int) []Measurement {
	return dc.buffer.GetLast(n)
}

// GetStats возвращает статистику по данным
func (dc *DataCollector) GetStats() (map[string]interface{}, error) {
	dbStats, err := dc.retention.GetStats()
	if err != nil {
		return nil, fmt.Errorf("статистика БД: %w", err)
	}

	dbStats["buffer_size"] = dc.buffer.Size()
	dbStats["buffer_max_size"] = dc.buffer.maxSize

	return dbStats, nil
}

// CollectAndStore - публичная обертка для collectAndStore
func (dc *DataCollector) CollectAndStore() error {
	return dc.collectAndStore()
}

// backgroundDataCollection запускает фоновый сбор данных с оптимизацией
func backgroundDataCollection(db *sqlx.DB, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	// Создаем оптимизированный коллектор с буферизацией
	collector := NewDataCollector(db)

	// Делаем первое измерение
	if err := collector.collectAndStore(); err != nil {
		log.Printf("⚠️ Первичное измерение: %v", err)
	}

	ticker := time.NewTicker(collector.pmsetInterval)
	defer ticker.Stop()

	log.Printf("🔄 Фоновый сбор данных запущен (pmset: %v, system_profiler: %v)",
		collector.pmsetInterval, collector.profilerInterval)

	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 Остановка фонового сбора данных")
			return
		case <-ticker.C:
			if err := collector.collectAndStore(); err != nil {
				log.Printf("⚠️ Ошибка сбора данных: %v", err)
				continue
			}

			// Логируем статистику иногда
			if collector.buffer.Size()%50 == 0 && collector.buffer.Size() > 0 {
				stats, err := collector.GetStats()
				if err == nil {
					log.Printf("📊 Статистика: буфер %d/%d, БД %v записей",
						stats["buffer_size"], stats["buffer_max_size"], stats["total_records"])
				}
			}

			// Адаптивная частота сбора данных
			latest := collector.GetLatestFromBuffer()
			if latest != nil {
				if strings.ToLower(latest.State) == "charging" && latest.Percentage >= 100 {
					log.Println("🔋 Батарея полностью заряжена, замедляем сбор данных")
					ticker.Reset(5 * time.Minute)
				} else if strings.ToLower(latest.State) == "discharging" {
					// Возвращаем нормальный интервал при разрядке
					ticker.Reset(collector.pmsetInterval)
				}
			}
		}
	}
}

// NewDataService создает новый сервис данных
func NewDataService(db *sqlx.DB, buffer *MemoryBuffer) *DataService {
	ctx, cancel := context.WithCancel(context.Background())
	
	// Используем существующую функцию NewDataCollector для правильной инициализации
	collector := NewDataCollector(db)
	// Заменяем буфер на наш
	collector.buffer = buffer
	
	return &DataService{
		collector: collector,
		db:        db,
		buffer:    buffer,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start запускает фоновый сбор данных
func (ds *DataService) Start() {
	ds.startCaffeinate()
	go ds.collectData()
}

// Stop останавливает сбор данных
func (ds *DataService) Stop() {
	ds.stopCaffeinate()
	ds.cancel()
}

// startCaffeinate запускает caffeinate для предотвращения засыпания
func (ds *DataService) startCaffeinate() {
	if ds.caffeineActive {
		return
	}
	
	// Используем -i флаг для предотвращения idle засыпания
	// Это не мешает засыпанию при закрытии крышки
	ds.caffeinate = exec.CommandContext(ds.ctx, "caffeinate", "-i")
	
	err := ds.caffeinate.Start()
	if err != nil {
		log.Printf("Предупреждение: не удалось запустить caffeinate: %v", err)
		return
	}
	
	ds.caffeineActive = true
	log.Println("✅ Предотвращение засыпания MacBook активировано")
	
	// Запускаем горутину для отслеживания завершения процесса
	go func() {
		ds.caffeinate.Wait()
		ds.caffeineActive = false
	}()
}

// stopCaffeinate останавливает caffeinate
func (ds *DataService) stopCaffeinate() {
	if !ds.caffeineActive || ds.caffeinate == nil {
		return
	}
	
	err := ds.caffeinate.Process.Kill()
	if err != nil {
		log.Printf("Предупреждение: не удалось остановить caffeinate: %v", err)
	} else {
		log.Println("🛌 Предотвращение засыпания MacBook отключено")
	}
	
	ds.caffeineActive = false
	ds.caffeinate = nil
}

// collectData выполняет фоновый сбор данных
func (ds *DataService) collectData() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ds.ctx.Done():
			return
		case <-ticker.C:
			// Собираем данные асинхронно
			go func() {
				if err := ds.collector.CollectAndStore(); err != nil {
					log.Printf("Ошибка сбора данных: %v", err)
				}
			}()
		}
	}
}

// GetLatest возвращает последнее измерение
func (ds *DataService) GetLatest() *Measurement {
	return ds.buffer.GetLatest()
}

// GetLast возвращает последние N измерений
func (ds *DataService) GetLast(n int) []Measurement {
	return ds.buffer.GetLast(n)
}