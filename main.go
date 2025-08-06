// main.go
//
// Консольная утилита batmon – мониторинг и оценка состояния батареи MacBook (Apple Silicon).
// Считывает данные о аккумуляторе, сохраняет их в SQLite и выводит отчёт.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

const (
	dbFile           = "batmon.sqlite"  // имя файла SQLite
	pmsetInterval    = 30 * time.Second // интервал опроса pmset
	profilerInterval = 2 * time.Minute  // интервал опроса system_profiler
)

// TrendAnalysis содержит результат анализа тренда
type TrendAnalysis struct {
	DegradationRate   float64 // процент деградации в месяц
	ProjectedLifetime int     // прогноз в днях до 80% емкости
	IsHealthy         bool    // соответствует ли деградация норме
}

// ChargeCycle представляет цикл заряда-разряда
type ChargeCycle struct {
	StartTime    time.Time
	EndTime      time.Time
	StartPercent int
	EndPercent   int
	CycleType    string // "discharge", "charge", "full_cycle"
	CapacityLoss int    // потеря емкости за цикл
}

// DataCollector управляет оптимизированным сбором данных
type DataCollector struct {
	db               *sqlx.DB
	buffer           *MemoryBuffer
	retention        *DataRetention
	lastProfilerCall time.Time
	pmsetInterval    time.Duration
	profilerInterval time.Duration
}

// ReportData содержит все данные для генерации отчета
type ReportData struct {
	GeneratedAt     time.Time
	Latest          Measurement
	Measurements    []Measurement
	HealthAnalysis  map[string]interface{}
	Wear            float64
	AvgRate         float64
	RobustRate      float64
	ValidIntervals  int
	RemainingTime   time.Duration
	Anomalies       []string
	Recommendations []string
}

// MemoryBuffer - буфер в памяти для быстрого доступа к последним измерениям
type MemoryBuffer struct {
	measurements    []Measurement
	maxSize         int
	mu              sync.RWMutex
	lastCleanup     time.Time
	cleanupInterval time.Duration
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

// shouldCleanup проверяет, нужна ли очистка старых данных
// DataRetention управляет ретенцией данных в БД
type DataRetention struct {
	db              *sqlx.DB
	retentionPeriod time.Duration
	lastCleanup     time.Time
	cleanupInterval time.Duration
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
	if dbFileInfo, err := os.Stat(dbFile); err == nil {
		stats["db_size_mb"] = float64(dbFileInfo.Size()) / (1024 * 1024)
	}

	return stats, nil
}

// analyzeAdvancedMetrics проводит анализ расширенных метрик батареи
func analyzeAdvancedMetrics(measurements []Measurement) AdvancedMetrics {
	if len(measurements) == 0 {
		return AdvancedMetrics{}
	}

	var metrics AdvancedMetrics
	latest := measurements[len(measurements)-1]

	// Анализируем стабильность напряжения
	voltages := make([]float64, 0)
	powers := make([]float64, 0)
	chargingEfficiencies := make([]float64, 0)

	for _, m := range measurements {
		if m.Voltage > 0 {
			voltages = append(voltages, float64(m.Voltage))
		}
		if m.Power != 0 {
			powers = append(powers, float64(m.Power))
		}

		// Эффективность зарядки (емкость / мощность)
		if m.Power > 0 && m.CurrentCapacity > 0 {
			efficiency := float64(m.CurrentCapacity) / float64(m.Power)
			chargingEfficiencies = append(chargingEfficiencies, efficiency)
		}
	}

	// Стабильность напряжения (коэффициент вариации)
	if len(voltages) > 1 {
		mean := 0.0
		for _, v := range voltages {
			mean += v
		}
		mean /= float64(len(voltages))

		variance := 0.0
		for _, v := range voltages {
			variance += (v - mean) * (v - mean)
		}
		variance /= float64(len(voltages))
		stdDev := math.Sqrt(variance)

		if mean > 0 {
			metrics.VoltageStability = 100 * (1 - stdDev/mean) // В процентах
		}
	}

	// Эффективность энергопотребления
	if len(powers) > 0 {
		avgPower := 0.0
		for _, p := range powers {
			avgPower += math.Abs(p) // Берем абсолютную величину
		}
		avgPower /= float64(len(powers))

		// Нормализуем эффективность (меньше мощность = выше эффективность)
		if avgPower > 0 {
			metrics.PowerEfficiency = math.Max(0, 100-avgPower/100)
		}
	}

	// Эффективность зарядки
	if len(chargingEfficiencies) > 0 {
		avgEfficiency := 0.0
		for _, e := range chargingEfficiencies {
			avgEfficiency += e
		}
		metrics.ChargingEfficiency = avgEfficiency / float64(len(chargingEfficiencies))
	}

	// Тренд энергопотребления
	if len(powers) >= 3 {
		recent := powers[len(powers)-3:]
		trend := "стабильное"

		if len(recent) == 3 {
			if recent[2] > recent[1] && recent[1] > recent[0] {
				trend = "растущее потребление"
			} else if recent[2] < recent[1] && recent[1] < recent[0] {
				trend = "снижающееся потребление"
			}
		}
		metrics.PowerTrend = trend
	}

	// Общий рейтинг здоровья
	healthScore := 100

	// Снижаем за износ
	if latest.DesignCapacity > 0 {
		wear := float64(latest.DesignCapacity-latest.FullChargeCap) / float64(latest.DesignCapacity) * 100
		healthScore -= int(wear * 0.5) // Износ влияет на 50%
	}

	// Снижаем за циклы
	cycleImpact := latest.CycleCount / 10 // Каждые 10 циклов = -1 балл
	healthScore -= cycleImpact

	// Снижаем за температуру
	if latest.Temperature > 45 {
		healthScore -= (latest.Temperature - 45) // Каждый градус свыше 45°C = -1 балл
	}

	// Учитываем стабильность напряжения
	if metrics.VoltageStability < 95 {
		healthScore -= int(95 - metrics.VoltageStability)
	}

	metrics.HealthRating = int(math.Max(0, float64(healthScore)))

	// Статус от Apple
	metrics.AppleStatus = latest.AppleCondition
	if metrics.AppleStatus == "" {
		if metrics.HealthRating >= 85 {
			metrics.AppleStatus = "Normal"
		} else if metrics.HealthRating >= 70 {
			metrics.AppleStatus = "Service Recommended"
		} else {
			metrics.AppleStatus = "Replace Soon"
		}
	}

	return metrics
}

// Measurement – запись о состоянии батареи.
type Measurement struct {
	ID              int    `db:"id"`
	Timestamp       string `db:"timestamp"`   // ISO‑8601 UTC
	Percentage      int    `db:"percentage"`  // % заряда
	State           string `db:"state"`       // charging / discharging
	CycleCount      int    `db:"cycle_count"` // кол-во циклов
	FullChargeCap   int    `db:"full_charge_capacity"`
	DesignCapacity  int    `db:"design_capacity"`
	CurrentCapacity int    `db:"current_capacity"`
	Temperature     int    `db:"temperature"` // температура батареи в °C
	// Расширенные метрики (Этап 6)
	Voltage        int    `db:"voltage"`         // Напряжение в мВ
	Amperage       int    `db:"amperage"`        // Ток в мА (+ заряд, - разряд)
	Power          int    `db:"power"`           // Мощность в мВт
	AppleCondition string `db:"apple_condition"` // Статус от Apple
}

// AdvancedMetrics содержит расширенные метрики анализа
type AdvancedMetrics struct {
	PowerEfficiency    float64 `json:"power_efficiency"`    // Эффективность энергопотребления
	VoltageStability   float64 `json:"voltage_stability"`   // Стабильность напряжения
	ChargingEfficiency float64 `json:"charging_efficiency"` // Эффективность зарядки
	PowerTrend         string  `json:"power_trend"`         // Тренд энергопотребления
	HealthRating       int     `json:"health_rating"`       // Общий рейтинг здоровья (0-100)
	AppleStatus        string  `json:"apple_status"`        // Статус от Apple (Normal, Replace Soon, etc.)
}

// initDB открывает соединение с SQLite и создаёт таблицу, если её нет.
func initDB(path string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("соединение с БД: %w", err)
	}

	// Включаем WAL режим для устранения блокировок при одновременном чтении/записи
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("предупреждение: не удалось включить WAL режим: %v", err)
	}

	schema := `CREATE TABLE IF NOT EXISTS measurements (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		percentage INTEGER,
		state TEXT,
		cycle_count INTEGER,
		full_charge_capacity INTEGER,
		design_capacity INTEGER,
		current_capacity INTEGER,
		temperature INTEGER DEFAULT 0,
		voltage INTEGER DEFAULT 0,
		amperage INTEGER DEFAULT 0,
		power INTEGER DEFAULT 0,
		apple_condition TEXT DEFAULT ''
	);`
	if _, err = db.Exec(schema); err != nil {
		return nil, fmt.Errorf("создание таблицы: %w", err)
	}

	// Добавляем новые столбцы к существующей таблице (для обновления схемы)
	alterQueries := []string{
		"ALTER TABLE measurements ADD COLUMN voltage INTEGER DEFAULT 0",
		"ALTER TABLE measurements ADD COLUMN amperage INTEGER DEFAULT 0",
		"ALTER TABLE measurements ADD COLUMN power INTEGER DEFAULT 0",
		"ALTER TABLE measurements ADD COLUMN apple_condition TEXT DEFAULT ''",
	}

	for _, query := range alterQueries {
		db.Exec(query) // Игнорируем ошибки - столбцы могут уже существовать
	}

	return db, nil
}

// parsePMSet получает процент заряда и состояние питания из pmset.
func parsePMSet() (int, string, error) {
	cmd := exec.Command("pmset", "-g", "batt")
	out, err := cmd.Output()
	if err != nil {
		return 0, "", fmt.Errorf("pmset: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	re := regexp.MustCompile(`(\d+)%\s*;\s*(\w+)`)
	for scanner.Scan() {
		line := scanner.Text()
		m := re.FindStringSubmatch(line)
		if len(m) == 3 {
			pct, _ := strconv.Atoi(m[1])
			state := strings.ToLower(m[2])
			return pct, state, nil
		}
	}
	if err = scanner.Err(); err != nil {
		return 0, "", fmt.Errorf("сканирование pmset: %w", err)
	}
	return 0, "", fmt.Errorf("данные о батарее не найдены")
}

// parseSystemProfiler получает данные из system_profiler.
// На Apple Silicon многие параметры недоступны, используем то, что есть
func parseSystemProfiler() (cycle, fullCap, designCap, currCap, temperature, voltage, amperage int, condition string, err error) {
	cmd := exec.Command("system_profiler", "SPPowerDataType", "-detailLevel", "full")
	out, cmdErr := cmd.Output()
	if cmdErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("system_profiler: %w", cmdErr)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Cycle Count:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "Cycle Count:"))
			cycle, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Condition:"):
			condition = strings.TrimSpace(strings.TrimPrefix(line, "Condition:"))
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("сканирование system_profiler: %w", scanErr)
	}
	return cycle, fullCap, designCap, currCap, temperature, voltage, amperage, condition, nil
}

// parseIORegistry получает подробные данные о батарее из ioreg
func parseIORegistry() (cycle, fullCap, designCap, currCap, temperature, voltage, amperage int, condition string, err error) {
	cmd := exec.Command("ioreg", "-rn", "AppleSmartBattery")
	out, cmdErr := cmd.Output()
	if cmdErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("ioreg: %w", cmdErr)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Парсим параметры в формате "ParameterName" = Value
		if strings.Contains(line, " = ") {
			parts := strings.SplitN(line, " = ", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.Trim(parts[0], `"`)
			value := strings.TrimSpace(parts[1])

			switch key {
			case "CycleCount":
				cycle, _ = strconv.Atoi(value)
			case "AppleRawMaxCapacity":
				fullCap, _ = strconv.Atoi(value)
			case "DesignCapacity":
				designCap, _ = strconv.Atoi(value)
			case "AppleRawCurrentCapacity":
				currCap, _ = strconv.Atoi(value)
			case "Temperature":
				// Температура в сотых долях градуса
				if temp, err := strconv.Atoi(value); err == nil {
					temperature = temp / 100
				}
			case "Voltage":
				voltage, _ = strconv.Atoi(value)
			case "Amperage":
				// Amperage может быть большим uint64, которое представляет отрицательное число
				if amp, err := strconv.ParseUint(value, 10, 64); err == nil {
					if amp > 9223372036854775807 { // больше максимального int64
						// Это отрицательное число, представленное как uint64
						amperage = int(int64(amp))
					} else {
						amperage = int(amp)
					}
				}
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("сканирование ioreg: %w", scanErr)
	}

	// Получаем состояние батареи из system_profiler
	spCycle, _, _, _, _, _, _, spCondition, spErr := parseSystemProfiler()
	if spErr == nil {
		condition = spCondition
		if cycle == 0 {
			cycle = spCycle
		}
	}

	return cycle, fullCap, designCap, currCap, temperature, voltage, amperage, condition, nil
}

// insertMeasurement сохраняет Measurement в БД.
func insertMeasurement(db *sqlx.DB, m *Measurement) error {
	query := `INSERT INTO measurements (
		timestamp, percentage, state, cycle_count,
		full_charge_capacity, design_capacity, current_capacity, temperature,
		voltage, amperage, power, apple_condition)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := db.Exec(query,
		m.Timestamp, m.Percentage, m.State, m.CycleCount,
		m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity, m.Temperature,
		m.Voltage, m.Amperage, m.Power, m.AppleCondition)
	return err
}

// getLastNMeasurements возвращает последние n измерений в хронологическом порядке.
func getLastNMeasurements(db *sqlx.DB, n int) ([]Measurement, error) {
	var ms []Measurement
	query := `SELECT * FROM measurements ORDER BY timestamp DESC LIMIT ?`
	if err := db.Select(&ms, query, n); err != nil {
		return nil, err
	}
	// Переворачиваем в возрастающий порядок по времени.
	for i, j := 0, len(ms)-1; i < j; i, j = i+1, j-1 {
		ms[i], ms[j] = ms[j], ms[i]
	}
	return ms, nil
}

// computeAvgRate вычисляет среднюю скорость разрядки (мАч/час) за последние n интервалов.
func computeAvgRate(ms []Measurement, intervals int) float64 {
	if len(ms) < 2 {
		return 0
	}
	start := len(ms) - intervals - 1
	if start < 0 {
		start = 0
	}

	var totalDiff, totalTime float64
	for i := start; i < len(ms)-1; i++ {
		diff := float64(ms[i].CurrentCapacity - ms[i+1].CurrentCapacity)
		if diff <= 0 { // зарядка или отсутствие изменения
			continue
		}
		t1, err1 := time.Parse(time.RFC3339, ms[i].Timestamp)
		t2, err2 := time.Parse(time.RFC3339, ms[i+1].Timestamp)
		if err1 != nil || err2 != nil {
			continue
		}
		timeH := t2.Sub(t1).Hours()
		totalDiff += diff
		totalTime += timeH
	}
	if totalTime == 0 {
		return 0
	}
	return totalDiff / totalTime
}

// computeRemainingTime оценивает оставшееся время работы в nanoseconds.
func computeRemainingTime(currentCap int, avgRate float64) time.Duration {
	if avgRate <= 0 {
		return 0
	}
	hours := float64(currentCap) / avgRate
	return time.Duration(hours * float64(time.Hour))
}

// computeWear рассчитывает процент износа батареи.
func computeWear(designCap, fullCap int) float64 {
	if designCap == 0 {
		return 0
	}
	return float64(designCap-fullCap) / float64(designCap) * 100.0
}

// detectBatteryAnomalies анализирует аномальные изменения заряда с нормализованными порогами
func detectBatteryAnomalies(ms []Measurement) []string {
	if len(ms) < 2 {
		return nil
	}

	var anomalies []string

	for i := 0; i < len(ms)-1; i++ {
		prev := ms[i]
		curr := ms[i+1]

		// Вычисляем интервал времени между измерениями
		prevTime, err1 := time.Parse(time.RFC3339, prev.Timestamp)
		currTime, err2 := time.Parse(time.RFC3339, curr.Timestamp)
		var interval time.Duration = 30 * time.Second // по умолчанию
		if err1 == nil && err2 == nil {
			interval = currTime.Sub(prevTime)
		}

		// Получаем нормализованные пороги
		chargeThreshold, capacityThreshold := normalizeAnomalyThresholds(interval)

		// Резкий скачок заряда
		chargeDiff := curr.Percentage - prev.Percentage
		if chargeDiff > chargeThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкий рост заряда: %d%% → %d%% за %.1f мин (%s)",
				prev.Percentage, curr.Percentage, interval.Minutes(), curr.Timestamp[11:19]))
		}

		// Резкое падение заряда
		if chargeDiff < -chargeThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкое падение заряда: %d%% → %d%% за %.1f мин (%s)",
				prev.Percentage, curr.Percentage, interval.Minutes(), curr.Timestamp[11:19]))
		}

		// Неожиданное изменение состояния
		if prev.State != curr.State {
			anomalies = append(anomalies, fmt.Sprintf("Смена состояния: %s → %s (%s)",
				prev.State, curr.State, curr.Timestamp[11:19]))
		}

		// Резкое изменение емкости
		capacityDiff := abs(curr.CurrentCapacity - prev.CurrentCapacity)
		if capacityDiff > capacityThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкое изменение емкости: %d → %d мАч за %.1f мин (%s)",
				prev.CurrentCapacity, curr.CurrentCapacity, interval.Minutes(), curr.Timestamp[11:19]))
		}
	}

	return anomalies
}

// computeAvgRateRobust вычисляет среднюю скорость с исключением аномалий
func computeAvgRateRobust(ms []Measurement, intervals int) (float64, int) {
	if len(ms) < 2 {
		return 0, 0
	}
	start := len(ms) - intervals - 1
	if start < 0 {
		start = 0
	}

	var totalDiff, totalTime float64
	validIntervals := 0

	for i := start; i < len(ms)-1; i++ {
		prev := ms[i]
		curr := ms[i+1]

		// Пропускаем аномальные изменения
		chargeDiff := abs(curr.Percentage - prev.Percentage)
		capacityDiff := abs(curr.CurrentCapacity - prev.CurrentCapacity)

		// Если резкое изменение заряда или емкости - пропускаем
		if chargeDiff > 20 || capacityDiff > 500 {
			continue
		}

		diff := float64(prev.CurrentCapacity - curr.CurrentCapacity)
		if diff <= 0 { // зарядка или отсутствие изменения
			continue
		}

		t1, err1 := time.Parse(time.RFC3339, prev.Timestamp)
		t2, err2 := time.Parse(time.RFC3339, curr.Timestamp)
		if err1 != nil || err2 != nil {
			continue
		}

		timeH := t2.Sub(t1).Hours()
		if timeH <= 0 || timeH > 2 { // Пропускаем слишком короткие или длинные интервалы
			continue
		}

		totalDiff += diff
		totalTime += timeH
		validIntervals++
	}

	if totalTime == 0 {
		return 0, validIntervals
	}
	return totalDiff / totalTime, validIntervals
}

// abs возвращает абсолютное значение
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// min возвращает минимальное значение
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max возвращает максимальное значение
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// analyzeCapacityTrend анализирует тренд деградации батареи
func analyzeCapacityTrend(measurements []Measurement) TrendAnalysis {
	if len(measurements) < 10 {
		return TrendAnalysis{IsHealthy: true} // Недостаточно данных для анализа
	}

	// Ищем измерения за последние 30 дней с system_profiler данными
	now := time.Now()
	thirtyDaysAgo := now.AddDate(0, 0, -30)

	var validMeasurements []Measurement
	for _, m := range measurements {
		if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
			if t.After(thirtyDaysAgo) && m.FullChargeCap > 0 && m.DesignCapacity > 0 {
				validMeasurements = append(validMeasurements, m)
			}
		}
	}

	if len(validMeasurements) < 5 {
		return TrendAnalysis{IsHealthy: true} // Недостаточно данных
	}

	// Простая линейная регрессия для тренда емкости
	first := validMeasurements[0]
	last := validMeasurements[len(validMeasurements)-1]

	firstTime, _ := time.Parse(time.RFC3339, first.Timestamp)
	lastTime, _ := time.Parse(time.RFC3339, last.Timestamp)

	daysDiff := lastTime.Sub(firstTime).Hours() / 24
	if daysDiff < 7 { // Менее недели данных
		return TrendAnalysis{IsHealthy: true}
	}

	capacityDiff := float64(last.FullChargeCap - first.FullChargeCap)
	dailyDegradation := capacityDiff / daysDiff
	monthlyDegradation := dailyDegradation * 30

	// Рассчитываем процент деградации от проектной емкости
	monthlyDegradationPercent := (monthlyDegradation / float64(last.DesignCapacity)) * 100

	// Прогноз времени до 80% емкости
	currentHealthPercent := (float64(last.FullChargeCap) / float64(last.DesignCapacity)) * 100
	targetHealthPercent := 80.0

	var projectedDays int
	if monthlyDegradationPercent < 0 && currentHealthPercent > targetHealthPercent {
		monthsTo80Percent := (currentHealthPercent - targetHealthPercent) / (-monthlyDegradationPercent)
		projectedDays = int(monthsTo80Percent * 30)
	}

	// Считаем здоровой деградацию менее 0.5% в месяц
	isHealthy := monthlyDegradationPercent > -0.5

	return TrendAnalysis{
		DegradationRate:   monthlyDegradationPercent,
		ProjectedLifetime: projectedDays,
		IsHealthy:         isHealthy,
	}
}

// detectChargeCycles обнаруживает циклы заряда-разряда
func detectChargeCycles(measurements []Measurement) []ChargeCycle {
	if len(measurements) < 3 {
		return nil
	}

	var cycles []ChargeCycle
	var currentCycle *ChargeCycle

	for i, m := range measurements {
		timestamp, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			continue
		}

		if i == 0 {
			continue // Пропускаем первое измерение
		}

		prev := measurements[i-1]

		// Определяем смену направления (заряд/разряд)
		if prev.State != m.State {
			if currentCycle != nil {
				// Завершаем текущий цикл
				currentCycle.EndTime = timestamp
				currentCycle.EndPercent = m.Percentage

				if prev.CurrentCapacity > 0 && m.CurrentCapacity > 0 {
					currentCycle.CapacityLoss = prev.CurrentCapacity - m.CurrentCapacity
				}

				cycles = append(cycles, *currentCycle)
			}

			// Начинаем новый цикл
			currentCycle = &ChargeCycle{
				StartTime:    timestamp,
				StartPercent: m.Percentage,
				CycleType:    strings.ToLower(m.State),
			}
		}

		// Обновляем текущий цикл
		if currentCycle != nil {
			currentCycle.EndTime = timestamp
			currentCycle.EndPercent = m.Percentage
		}
	}

	// Завершаем последний цикл если есть
	if currentCycle != nil {
		cycles = append(cycles, *currentCycle)
	}

	return cycles
}

// normalizeAnomalyThresholds нормализует пороги аномалий на время
func normalizeAnomalyThresholds(interval time.Duration) (int, int) {
	// Базовые пороги для 30-секундного интервала
	baseChargeThreshold := 20    // процентов
	baseCapacityThreshold := 500 // мАч

	// Нормализация на минуту
	minutes := interval.Minutes()
	if minutes < 0.5 {
		minutes = 0.5 // минимум 30 секунд
	}

	// Чем больше интервал, тем выше допустимые пороги
	normalizedChargeThreshold := int(float64(baseChargeThreshold) * minutes * 2) // 40% в минуту
	normalizedCapacityThreshold := int(float64(baseCapacityThreshold) * minutes)

	// Ограничиваем максимальные пороги
	if normalizedChargeThreshold > 50 {
		normalizedChargeThreshold = 50
	}
	if normalizedCapacityThreshold > 2000 {
		normalizedCapacityThreshold = 2000
	}

	return normalizedChargeThreshold, normalizedCapacityThreshold
}

// printColoredStatus выводит статус с цветовым оформлением
func printColoredStatus(status string, value interface{}, level string) {
	switch level {
	case "critical":
		color.Red("%s: %v", status, value)
	case "warning":
		color.Yellow("%s: %v", status, value)
	case "good":
		color.Green("%s: %v", status, value)
	case "info":
		color.Cyan("%s: %v", status, value)
	default:
		fmt.Printf("%s: %v\n", status, value)
	}
}

// getStatusLevel определяет уровень важности для цветового оформления
func getStatusLevel(wear float64, percentage int, temperature int, healthScore int) string {
	if wear > 30 || percentage < 10 || temperature > 45 || healthScore < 40 {
		return "critical"
	}
	if wear > 20 || percentage < 20 || temperature > 35 || healthScore < 70 {
		return "warning"
	}
	if wear < 10 && percentage > 50 && temperature < 30 && healthScore > 85 {
		return "good"
	}
	return "info"
}

// formatStateWithEmoji добавляет эмодзи к состоянию батареи
func formatStateWithEmoji(state string, percentage int) string {
	if state == "" {
		return "Неизвестно"
	}

	stateLower := strings.ToLower(state)
	stateFormatted := strings.ToUpper(string(stateLower[0])) + stateLower[1:]

	switch stateLower {
	case "charging":
		if percentage >= 90 {
			return "🔋 " + stateFormatted + " (почти полная)"
		}
		return "⚡ " + stateFormatted
	case "discharging":
		if percentage < 20 {
			return "🪫 " + stateFormatted + " (низкий заряд)"
		} else if percentage < 50 {
			return "🔋 " + stateFormatted
		}
		return "🔋 " + stateFormatted
	case "charged":
		return "✅ " + stateFormatted
	case "finishing":
		return "🔌 " + stateFormatted
	default:
		return stateFormatted
	}
}

// analyzeBatteryHealth анализирует общее состояние батареи
func analyzeBatteryHealth(ms []Measurement) map[string]interface{} {
	if len(ms) == 0 {
		return nil
	}

	latest := ms[len(ms)-1]
	analysis := make(map[string]interface{})

	// Основные метрики
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)
	analysis["wear_percentage"] = wear
	analysis["cycle_count"] = latest.CycleCount

	// Анализ аномалий
	anomalies := detectBatteryAnomalies(ms)
	analysis["anomalies"] = anomalies
	analysis["anomaly_count"] = len(anomalies)

	// Робастная скорость разрядки
	avgRate, validIntervals := computeAvgRateRobust(ms, 10)
	analysis["discharge_rate"] = avgRate
	analysis["valid_intervals"] = validIntervals

	// Анализ трендов
	trendAnalysis := analyzeCapacityTrend(ms)
	analysis["trend_analysis"] = trendAnalysis

	// Анализ циклов заряда-разряда
	chargeCycles := detectChargeCycles(ms)
	analysis["charge_cycles"] = chargeCycles

	// Оценка здоровья батареи
	var healthStatus string
	var healthScore int

	switch {
	case wear < 5 && latest.CycleCount < 300:
		healthStatus = "Отличное"
		healthScore = 95
	case wear < 10 && latest.CycleCount < 500:
		healthStatus = "Хорошее"
		healthScore = 85
	case wear < 20 && latest.CycleCount < 800:
		healthStatus = "Удовлетворительное"
		healthScore = 70
	case wear < 30 && latest.CycleCount < 1200:
		healthStatus = "Требует внимания"
		healthScore = 50
	default:
		healthStatus = "Плохое"
		healthScore = 30
	}

	// Корректировка на основе аномалий
	if len(anomalies) > 5 {
		healthScore -= 10
		healthStatus += " (нестабильная работа)"
	}

	// Корректировка на основе тренда
	if !trendAnalysis.IsHealthy && trendAnalysis.DegradationRate < -1.0 {
		healthScore -= 15
		healthStatus += " (быстрая деградация)"
	}

	analysis["health_status"] = healthStatus
	analysis["health_score"] = healthScore

	// Расширенные рекомендации
	var recommendations []string

	// Рекомендации по замене
	if wear > 20 {
		recommendations = append(recommendations, "Рассмотрите замену батареи")
	}

	// Рекомендации по аномалиям
	if len(anomalies) > 3 {
		recommendations = append(recommendations, "Проверьте настройки энергосбережения")
	}

	// Рекомендации по циклам
	if latest.CycleCount > 1000 {
		recommendations = append(recommendations, "Батарея приближается к концу жизненного цикла")
	}

	// Рекомендации по энергопотреблению
	if avgRate > 1000 {
		recommendations = append(recommendations, "Высокое энергопотребление - закройте ресурсоемкие приложения")
	}

	// Рекомендации по температуре
	if latest.Temperature > 40 {
		recommendations = append(recommendations, "Высокая температура батареи ("+strconv.Itoa(latest.Temperature)+"°C) - избегайте нагрузки")
	} else if latest.Temperature > 35 {
		recommendations = append(recommendations, "Повышенная температура батареи - рассмотрите улучшение охлаждения")
	}

	// Рекомендации по трендам
	if !trendAnalysis.IsHealthy && trendAnalysis.DegradationRate < -0.5 {
		recommendations = append(recommendations, fmt.Sprintf("Быстрая деградация батареи (%.2f%% в месяц) - проверьте условия эксплуатации", -trendAnalysis.DegradationRate))
	}

	// Рекомендации по заряду
	if latest.State == "charging" && latest.Percentage == 100 {
		recommendations = append(recommendations, "Не держите батарею постоянно на 100% заряда")
	}

	// Рекомендации по калибровке
	if wear > 15 && latest.CycleCount > 500 {
		recommendations = append(recommendations, "Рассмотрите калибровку батареи (полный разряд и заряд)")
	}

	analysis["recommendations"] = recommendations

	return analysis
}

// exportToMarkdown экспортирует отчет в формате Markdown
func exportToMarkdown(data ReportData, filename string) error {
	content := fmt.Sprintf(`# 🔋 Отчет о состоянии батареи MacBook

**Дата создания:** %s

## 💼 Краткое резюме

`, data.GeneratedAt.Format("02.01.2006 15:04:05"))

	if data.HealthAnalysis != nil {
		if status, ok := data.HealthAnalysis["health_status"].(string); ok {
			score, _ := data.HealthAnalysis["health_score"].(int)
			content += fmt.Sprintf("- **Здоровье батареи:** %s (рейтинг %d/100)\n", status, score)
		}
	}
	content += fmt.Sprintf("- **Циклы:** %d\n", data.Latest.CycleCount)
	content += fmt.Sprintf("- **Износ:** %.1f%%\n", data.Wear)
	if data.RemainingTime > 0 {
		content += fmt.Sprintf("- **Оставшееся время:** %s\n", data.RemainingTime.Truncate(time.Minute))
	}

	content += fmt.Sprintf(`
## 🔋 Текущее состояние батареи

| Параметр | Значение |
|----------|----------|
| Время измерения | %s |
| Заряд | %d%% |
| Состояние | %s |
| Циклы зарядки | %d |
| Полная ёмкость | %d мАч |
| Проектная ёмкость | %d мАч |
| Текущая ёмкость | %d мАч |
`,
		data.Latest.Timestamp,
		data.Latest.Percentage,
		formatStateForExport(data.Latest.State, data.Latest.Percentage),
		data.Latest.CycleCount,
		data.Latest.FullChargeCap,
		data.Latest.DesignCapacity,
		data.Latest.CurrentCapacity)

	if data.Latest.Temperature > 0 {
		content += fmt.Sprintf("| Температура | %d°C |\n", data.Latest.Temperature)
	}

	content += "\n## 📊 Анализ здоровья батареи\n\n"
	if data.HealthAnalysis != nil {
		if status, ok := data.HealthAnalysis["health_status"].(string); ok {
			score, _ := data.HealthAnalysis["health_score"].(int)
			content += fmt.Sprintf("**Общее состояние:** %s (оценка: %d/100)\n\n", status, score)
		}
		content += fmt.Sprintf("**Износ батареи:** %.1f%%\n\n", data.Wear)

		// Анализ трендов
		if trendAnalysis, ok := data.HealthAnalysis["trend_analysis"].(TrendAnalysis); ok {
			if trendAnalysis.DegradationRate != 0 {
				content += fmt.Sprintf("**Тренд деградации:** %.2f%% в месяц\n\n", trendAnalysis.DegradationRate)
				if trendAnalysis.ProjectedLifetime > 0 {
					content += fmt.Sprintf("**Прогноз до 80%% емкости:** ~%d дней\n\n", trendAnalysis.ProjectedLifetime)
				}
			}
		}

		if len(data.Anomalies) > 0 {
			content += fmt.Sprintf("### ⚠️ Обнаруженные аномалии (%d)\n\n", len(data.Anomalies))
			for i, anomaly := range data.Anomalies {
				if i >= 10 { // Показываем максимум 10 аномалий в экспорте
					content += fmt.Sprintf("... и еще %d аномалий\n\n", len(data.Anomalies)-i)
					break
				}
				content += fmt.Sprintf("- %s\n", anomaly)
			}
			content += "\n"
		}

		if len(data.Recommendations) > 0 {
			content += "### 💡 Рекомендации\n\n"
			for _, rec := range data.Recommendations {
				content += fmt.Sprintf("- %s\n", rec)
			}
			content += "\n"
		}
	}

	content += "## 📈 Статистика разрядки\n\n"
	if data.AvgRate > 0 {
		content += fmt.Sprintf("- **Простая скорость разрядки:** %.2f мАч/час\n", data.AvgRate)
	}
	if data.RobustRate > 0 {
		content += fmt.Sprintf("- **Робастная скорость разрядки:** %.2f мАч/час (на основе %d валидных интервалов)\n", data.RobustRate, data.ValidIntervals)
	}
	if data.RemainingTime > 0 {
		content += fmt.Sprintf("- **Оставшееся время работы:** %s\n", data.RemainingTime.Truncate(time.Minute))
	}

	content += "\n## 📋 Последние измерения\n\n"
	content += "| Время | Заряд | Состояние | Цикл | Полная емк. | Проект. емк. | Текущ. емк. | Темп. |\n"
	content += "|-------|-------|-----------|------|-------------|--------------|-------------|-------|\n"

	startIdx := 0
	if len(data.Measurements) > 15 {
		startIdx = len(data.Measurements) - 15 // Показываем последние 15 в экспорте
	}

	for i := startIdx; i < len(data.Measurements); i++ {
		if i < 0 {
			continue
		}
		m := data.Measurements[i]
		timeStr := m.Timestamp[11:19] // только время
		tempStr := "-"
		if m.Temperature > 0 {
			tempStr = fmt.Sprintf("%d°C", m.Temperature)
		}

		content += fmt.Sprintf("| %s | %d%% | %s | %d | %d | %d | %d | %s |\n",
			timeStr, m.Percentage, formatStateForExport(m.State, m.Percentage),
			m.CycleCount, m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity, tempStr)
	}

	content += "\n---\n*Отчет сгенерирован утилитой batmon v2.0*\n"

	return os.WriteFile(filename, []byte(content), 0644)
}

// exportToHTML экспортирует отчет в формате HTML с графиками
func exportToHTML(data ReportData, filename string) error {
	tmpl := `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>🔋 Отчет о состоянии батареи MacBook</title>
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; 
            margin: 40px; 
            background-color: #f5f5f7; 
            color: #1d1d1f;
        }
        .container { 
            max-width: 1200px; 
            margin: 0 auto; 
            background: white; 
            padding: 40px; 
            border-radius: 12px; 
            box-shadow: 0 4px 20px rgba(0,0,0,0.1);
        }
        .header { 
            text-align: center; 
            margin-bottom: 40px; 
            padding-bottom: 20px;
            border-bottom: 2px solid #e5e5e7;
        }
        .summary { 
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); 
            color: white; 
            padding: 30px; 
            border-radius: 12px; 
            margin-bottom: 30px; 
        }
        .grid { 
            display: grid; 
            grid-template-columns: 1fr 1fr; 
            gap: 30px; 
            margin-bottom: 30px; 
        }
        .card { 
            background: #f8f9fa; 
            padding: 25px; 
            border-radius: 8px; 
            border: 1px solid #e9ecef;
        }
        .status-good { color: #28a745; font-weight: bold; }
        .status-warning { color: #ffc107; font-weight: bold; }
        .status-critical { color: #dc3545; font-weight: bold; }
        table { 
            width: 100%; 
            border-collapse: collapse; 
            margin-top: 20px; 
        }
        th, td { 
            padding: 12px; 
            text-align: left; 
            border-bottom: 1px solid #ddd; 
        }
        th { 
            background-color: #f8f9fa; 
            font-weight: 600;
        }
        .chart-container { 
            position: relative; 
            height: 400px; 
            margin: 20px 0; 
        }
        .anomaly { 
            background: #fff3cd; 
            border: 1px solid #ffeaa7; 
            padding: 15px; 
            border-radius: 6px; 
            margin: 10px 0; 
        }
        .recommendation { 
            background: #d1edff; 
            border: 1px solid #74b9ff; 
            padding: 15px; 
            border-radius: 6px; 
            margin: 10px 0; 
        }
        .footer { 
            text-align: center; 
            margin-top: 40px; 
            padding-top: 20px; 
            border-top: 1px solid #e5e5e7; 
            color: #86868b; 
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🔋 Отчет о состоянии батареи MacBook</h1>
            <p>Дата создания: {{.GeneratedAt.Format "02.01.2006 15:04:05"}}</p>
        </div>

        <div class="summary">
            <h2>💼 Краткое резюме</h2>
            {{if .HealthAnalysis}}
                {{if index .HealthAnalysis "health_status"}}
                    <p>🏥 <strong>Здоровье батареи:</strong> {{index .HealthAnalysis "health_status"}} (рейтинг {{index .HealthAnalysis "health_score"}}/100)</p>
                {{end}}
            {{end}}
            <p>🔄 <strong>Циклы:</strong> {{.Latest.CycleCount}}</p>
            <p>📉 <strong>Износ:</strong> {{printf "%.1f" .Wear}}%</p>
            {{if gt .RemainingTime 0}}
                <p>⏰ <strong>Оставшееся время:</strong> {{.RemainingTime.Truncate 1000000000}}</p>
            {{end}}
        </div>

        <div class="grid">
            <div class="card">
                <h3>📊 Графики</h3>
                <div class="chart-container">
                    <canvas id="batteryChart"></canvas>
                </div>
                <div class="chart-container">
                    <canvas id="capacityChart"></canvas>
                </div>
            </div>

            <div class="card">
                <h3>🔋 Текущее состояние</h3>
                <table>
                    <tr><td><strong>Заряд</strong></td><td>{{.Latest.Percentage}}%</td></tr>
                    <tr><td><strong>Состояние</strong></td><td>{{.Latest.State}}</td></tr>
                    <tr><td><strong>Циклы</strong></td><td>{{.Latest.CycleCount}}</td></tr>
                    <tr><td><strong>Полная ёмкость</strong></td><td>{{.Latest.FullChargeCap}} мАч</td></tr>
                    <tr><td><strong>Проектная ёмкость</strong></td><td>{{.Latest.DesignCapacity}} мАч</td></tr>
                    <tr><td><strong>Текущая ёмкость</strong></td><td>{{.Latest.CurrentCapacity}} мАч</td></tr>
                    {{if gt .Latest.Temperature 0}}
                        <tr><td><strong>Температура</strong></td><td>{{.Latest.Temperature}}°C</td></tr>
                    {{end}}
                </table>
            </div>
        </div>

        {{if .Anomalies}}
        <div class="card">
            <h3>⚠️ Обнаруженные аномалии ({{len .Anomalies}})</h3>
            {{range $index, $anomaly := .Anomalies}}
                {{if lt $index 10}}
                    <div class="anomaly">{{$anomaly}}</div>
                {{end}}
            {{end}}
            {{if gt (len .Anomalies) 10}}
                <p>... и еще {{sub (len .Anomalies) 10}} аномалий</p>
            {{end}}
        </div>
        {{end}}

        {{if .Recommendations}}
        <div class="card">
            <h3>💡 Рекомендации</h3>
            {{range .Recommendations}}
                <div class="recommendation">{{.}}</div>
            {{end}}
        </div>
        {{end}}

        <div class="card">
            <h3>📋 Последние измерения</h3>
            <table>
                <thead>
                    <tr>
                        <th>Время</th>
                        <th>Заряд</th>
                        <th>Состояние</th>
                        <th>Цикл</th>
                        <th>Полная емк.</th>
                        <th>Текущ. емк.</th>
                        <th>Темп.</th>
                    </tr>
                </thead>
                <tbody>
                    {{$len := len .Measurements}}
                    {{$start := 0}}
                    {{if gt $len 15}}
                        {{$start = sub $len 15}}
                    {{end}}
                    {{range $index, $m := .Measurements}}
                        {{if ge $index $start}}
                            <tr>
                                <td>{{slice $m.Timestamp 11 19}}</td>
                                <td>{{$m.Percentage}}%</td>
                                <td>{{$m.State}}</td>
                                <td>{{$m.CycleCount}}</td>
                                <td>{{$m.FullChargeCap}} мАч</td>
                                <td>{{$m.CurrentCapacity}} мАч</td>
                                <td>{{if gt $m.Temperature 0}}{{$m.Temperature}}°C{{else}}-{{end}}</td>
                            </tr>
                        {{end}}
                    {{end}}
                </tbody>
            </table>
        </div>

        <div class="footer">
            <p><em>Отчет сгенерирован утилитой batmon v2.0</em></p>
        </div>
    </div>

    <script>
        // График заряда батареи
        const batteryCtx = document.getElementById('batteryChart').getContext('2d');
        const batteryData = [
            {{range $index, $m := .Measurements}}
                {{if lt $index 20}}{{$m.Percentage}},{{end}}
            {{end}}
        ];
        
        new Chart(batteryCtx, {
            type: 'line',
            data: {
                labels: [
                    {{range $index, $m := .Measurements}}
                        {{if lt $index 20}}'{{slice $m.Timestamp 11 19}}',{{end}}
                    {{end}}
                ],
                datasets: [{
                    label: 'Заряд (%)',
                    data: batteryData,
                    borderColor: '#28a745',
                    backgroundColor: 'rgba(40, 167, 69, 0.1)',
                    fill: true,
                    tension: 0.4
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    title: {
                        display: true,
                        text: 'Заряд батареи (%)'
                    }
                },
                scales: {
                    y: {
                        beginAtZero: true,
                        max: 100
                    }
                }
            }
        });

        // График емкости
        const capacityCtx = document.getElementById('capacityChart').getContext('2d');
        const capacityData = [
            {{range $index, $m := .Measurements}}
                {{if lt $index 20}}{{$m.CurrentCapacity}},{{end}}
            {{end}}
        ];
        
        new Chart(capacityCtx, {
            type: 'line',
            data: {
                labels: [
                    {{range $index, $m := .Measurements}}
                        {{if lt $index 20}}'{{slice $m.Timestamp 11 19}}',{{end}}
                    {{end}}
                ],
                datasets: [{
                    label: 'Емкость (мАч)',
                    data: capacityData,
                    borderColor: '#007bff',
                    backgroundColor: 'rgba(0, 123, 255, 0.1)',
                    fill: true,
                    tension: 0.4
                }]
            },
            options: {
                responsive: true,
                maintainAspectRatio: false,
                plugins: {
                    title: {
                        display: true,
                        text: 'Текущая емкость (мАч)'
                    }
                }
            }
        });
    </script>
</body>
</html>`

	// Добавляем вспомогательные функции для шаблона
	funcMap := template.FuncMap{
		"sub": func(a, b int) int {
			return a - b
		},
	}

	t, err := template.New("report").Funcs(funcMap).Parse(tmpl)
	if err != nil {
		return fmt.Errorf("парсинг шаблона: %w", err)
	}

	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("создание файла: %w", err)
	}
	defer file.Close()

	return t.Execute(file, data)
}

// formatStateForExport форматирует состояние батареи для экспорта (без эмодзи)
func formatStateForExport(state string, percentage int) string {
	if state == "" {
		return "Неизвестно"
	}

	stateLower := strings.ToLower(state)
	stateFormatted := strings.ToUpper(string(stateLower[0])) + stateLower[1:]

	switch stateLower {
	case "charging":
		if percentage >= 90 {
			return stateFormatted + " (почти полная)"
		}
		return stateFormatted
	case "discharging":
		if percentage < 20 {
			return stateFormatted + " (низкий заряд)"
		}
		return stateFormatted
	case "charged":
		return stateFormatted
	case "finishing":
		return stateFormatted
	default:
		return stateFormatted
	}
}

// generateReportData собирает данные для отчета
func generateReportData(db *sqlx.DB) (ReportData, error) {
	ms, err := getLastNMeasurements(db, 50)
	if err != nil {
		return ReportData{}, fmt.Errorf("получение данных: %w", err)
	}
	if len(ms) == 0 {
		return ReportData{}, fmt.Errorf("нет данных для отчета")
	}

	latest := ms[len(ms)-1]
	avgRate := computeAvgRate(ms, 5)
	robustRate, validIntervals := computeAvgRateRobust(ms, 10)
	remaining := computeRemainingTime(latest.CurrentCapacity, robustRate)
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)
	healthAnalysis := analyzeBatteryHealth(ms)

	var anomalies []string
	var recommendations []string

	if healthAnalysis != nil {
		if anomaliesList, ok := healthAnalysis["anomalies"].([]string); ok {
			anomalies = anomaliesList
		}
		if recsList, ok := healthAnalysis["recommendations"].([]string); ok {
			recommendations = recsList
		}
	}

	return ReportData{
		GeneratedAt:     time.Now(),
		Latest:          latest,
		Measurements:    ms,
		HealthAnalysis:  healthAnalysis,
		Wear:            wear,
		AvgRate:         avgRate,
		RobustRate:      robustRate,
		ValidIntervals:  validIntervals,
		RemainingTime:   remaining,
		Anomalies:       anomalies,
		Recommendations: recommendations,
	}, nil
}

// isOnBattery проверяет, работает ли система от батареи
func isOnBattery() (bool, string, int, error) {
	pct, state, err := parsePMSet()
	if err != nil {
		return false, "", 0, err
	}

	isOnBatt := strings.ToLower(state) == "discharging" ||
		strings.ToLower(state) == "finishing" ||
		strings.ToLower(state) == "charged"

	return isOnBatt, state, pct, nil
}

// backgroundDataCollection запускает сбор данных в фоне с оптимизацией частоты
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

// normalizeKeyInput нормализует ввод клавиш для поддержки разных раскладок клавиатуры
func normalizeKeyInput(keyID string) string {
	// Карта соответствий клавиш в разных раскладках к стандартным английским
	keyMappings := map[string]string{
		// Русская раскладка (ЙЦУКЕН)
		"й": "q", // q -> й
		"ц": "w", // w -> ц
		"у": "e", // e -> у
		"к": "r", // r -> к
		"е": "t", // t -> е
		"н": "y", // y -> н
		"г": "u", // u -> г
		"ш": "i", // i -> ш
		"щ": "o", // o -> щ
		"з": "p", // p -> з
		"ф": "a", // a -> ф
		"ы": "s", // s -> ы
		"в": "d", // d -> в
		"а": "f", // f -> а
		"п": "g", // g -> п
		"р": "h", // h -> р
		"о": "j", // j -> о
		"л": "k", // k -> л
		"д": "l", // l -> д
		"я": "z", // z -> я
		"ч": "x", // x -> ч
		"с": "c", // c -> с
		"м": "v", // v -> м
		"и": "b", // b -> и
		"т": "n", // n -> т
		"ь": "m", // m -> ь

		// Немецкая раскладка (QWERTZ) - только проблемные клавиши
		"ü": "y", // В немецкой y на месте ü
		"ä": "a", // и т.д.
	}

	// Проверяем, есть ли маппинг для данной клавиши
	if normalized, exists := keyMappings[keyID]; exists {
		return normalized
	}

	// Если маппинга нет, возвращаем исходную клавишу
	return keyID
}

// DashboardLayout содержит размеры и позиции всех виджетов дашборда
type DashboardLayout struct {
	BatteryChart  struct{ X1, Y1, X2, Y2 int }
	CapacityChart struct{ X1, Y1, X2, Y2 int }
	InfoList      struct{ X1, Y1, X2, Y2 int }
	StateGauge    struct{ X1, Y1, X2, Y2 int }
	WearGauge     struct{ X1, Y1, X2, Y2 int }
	Table         struct{ X1, Y1, X2, Y2 int }
}

// calculateLayout вычисляет адаптивный лейаут в зависимости от размера терминала
func calculateLayout() DashboardLayout {
	termWidth, termHeight := ui.TerminalDimensions()

	var layout DashboardLayout

	// Для очень маленьких терминалов - упрощенный лейаут
	if termWidth < 60 || termHeight < 20 {
		// Минимальные размеры для упрощенного лейаута
		if termWidth < 40 {
			termWidth = 40
		}
		if termHeight < 15 {
			termHeight = 15
		}

		// Упрощенный лейаут: только основные элементы
		halfHeight := termHeight / 2

		layout.BatteryChart.X1 = 0
		layout.BatteryChart.Y1 = 0
		layout.BatteryChart.X2 = termWidth
		layout.BatteryChart.Y2 = halfHeight

		layout.InfoList.X1 = 0
		layout.InfoList.Y1 = halfHeight
		layout.InfoList.X2 = termWidth
		layout.InfoList.Y2 = termHeight

		// Остальные виджеты скрываем (нулевые размеры)
		layout.CapacityChart = layout.BatteryChart // Дублируем чтобы не было ошибок
		layout.StateGauge.X1 = 0
		layout.StateGauge.Y1 = 0
		layout.StateGauge.X2 = 0
		layout.StateGauge.Y2 = 0
		layout.WearGauge.X1 = 0
		layout.WearGauge.Y1 = 0
		layout.WearGauge.X2 = 0
		layout.WearGauge.Y2 = 0
		layout.Table.X1 = 0
		layout.Table.Y1 = 0
		layout.Table.X2 = 0
		layout.Table.Y2 = 0

		return layout
	}

	// Стандартные минимальные размеры
	if termWidth < 80 {
		termWidth = 80
	}
	if termHeight < 25 {
		termHeight = 25
	}

	// Рассчитываем размеры относительно терминала
	leftWidth := termWidth / 2
	topHeight := (termHeight * 3) / 5 // 60% высоты для графиков
	bottomHeight := termHeight - topHeight

	// Убеждаемся, что нижняя область имеет минимальную высоту
	if bottomHeight < 6 {
		topHeight = termHeight - 6
		bottomHeight = 6
	}

	// График заряда батареи (левый верхний)
	layout.BatteryChart.X1 = 0
	layout.BatteryChart.Y1 = 0
	layout.BatteryChart.X2 = leftWidth
	layout.BatteryChart.Y2 = topHeight

	// График ёмкости (правый верхний) - добавляем отступ от левой колонки
	layout.CapacityChart.X1 = leftWidth + 1
	layout.CapacityChart.Y1 = 0
	layout.CapacityChart.X2 = termWidth
	layout.CapacityChart.Y2 = topHeight

	// Информационный список (левый нижний) - уменьшаем правую границу на 1 символ
	layout.InfoList.X1 = 0
	layout.InfoList.Y1 = topHeight
	layout.InfoList.X2 = leftWidth - 1
	layout.InfoList.Y2 = termHeight

	// Правая нижняя область: лучше разделить с минимальными размерами
	gaugeHeight := max(4, bottomHeight/3) // Минимум 4 строки для каждого gauge

	// Убеждаемся, что все виджеты помещаются
	if gaugeHeight*2+6 > bottomHeight { // 6 = минимум для таблицы
		gaugeHeight = max(4, (bottomHeight-6)/3) // Сжимаем gauges если не помещается, но не меньше 4
	}

	// Гистограмма заряда - добавляем отступ от левой колонки
	layout.StateGauge.X1 = leftWidth + 1
	layout.StateGauge.Y1 = topHeight
	layout.StateGauge.X2 = termWidth
	layout.StateGauge.Y2 = topHeight + gaugeHeight

	// Гистограмма износа - добавляем отступ от левой колонки
	layout.WearGauge.X1 = leftWidth + 1
	layout.WearGauge.Y1 = topHeight + gaugeHeight
	layout.WearGauge.X2 = termWidth
	layout.WearGauge.Y2 = topHeight + 2*gaugeHeight

	// Таблица последних измерений - добавляем отступ от левой колонки
	layout.Table.X1 = leftWidth + 1
	layout.Table.Y1 = topHeight + 2*gaugeHeight
	layout.Table.X2 = termWidth
	layout.Table.Y2 = termHeight

	return layout
}

// applyLayout применяет рассчитанный лейаут к виджетам
func applyLayout(layout DashboardLayout, batteryChart, capacityChart *widgets.Plot,
	infoList *widgets.List, stateGauge, wearGauge *widgets.Gauge, table *widgets.Table) {

	// Всегда устанавливаем основные виджеты
	batteryChart.SetRect(layout.BatteryChart.X1, layout.BatteryChart.Y1,
		layout.BatteryChart.X2, layout.BatteryChart.Y2)
	infoList.SetRect(layout.InfoList.X1, layout.InfoList.Y1,
		layout.InfoList.X2, layout.InfoList.Y2)

	// Устанавливаем дополнительные виджеты только если у них есть размеры
	if layout.CapacityChart.X2 > layout.CapacityChart.X1 && layout.CapacityChart.Y2 > layout.CapacityChart.Y1 {
		capacityChart.SetRect(layout.CapacityChart.X1, layout.CapacityChart.Y1,
			layout.CapacityChart.X2, layout.CapacityChart.Y2)
	}
	if layout.StateGauge.X2 > layout.StateGauge.X1 && layout.StateGauge.Y2 > layout.StateGauge.Y1 {
		stateGauge.SetRect(layout.StateGauge.X1, layout.StateGauge.Y1,
			layout.StateGauge.X2, layout.StateGauge.Y2)
	}
	if layout.WearGauge.X2 > layout.WearGauge.X1 && layout.WearGauge.Y2 > layout.WearGauge.Y1 {
		wearGauge.SetRect(layout.WearGauge.X1, layout.WearGauge.Y1,
			layout.WearGauge.X2, layout.WearGauge.Y2)
	}
	if layout.Table.X2 > layout.Table.X1 && layout.Table.Y2 > layout.Table.Y1 {
		table.SetRect(layout.Table.X1, layout.Table.Y1,
			layout.Table.X2, layout.Table.Y2)
	}
}

// getDashboardHotkeys возвращает подсказки по горячим клавишам для дашборда
func getDashboardHotkeys() []string {
	return []string{
		"",
		"═══ ГОРЯЧИЕ КЛАВИШИ ═══",
		"⌨️  'q'/'й' / Ctrl+C - Выход",
		"🔄 'r'/'к' - Обновить данные",
		"📊 'h'/'р' - Показать справку",
		"📈 Автообновление: каждые 10с",
		"🌍 Поддержка русской раскладки",
	}
}

// safeUpdateChartData безопасно обновляет данные графиков с проверками
func safeUpdateChartData(batteryChart, capacityChart *widgets.Plot, measurements []Measurement) {
	if len(measurements) == 0 {
		// Если данных нет, создаем минимальные данные для отображения
		batteryChart.Data[0] = []float64{0, 0}
		capacityChart.Data[0] = []float64{0, 0}
		return
	}

	dataSize := len(measurements)
	if dataSize < 2 {
		// Дублируем единственную точку для корректной отрисовки
		batteryChart.Data[0] = make([]float64, 2)
		batteryChart.Data[0][0] = float64(measurements[0].Percentage)
		batteryChart.Data[0][1] = float64(measurements[0].Percentage)
		capacityChart.Data[0] = make([]float64, 2)
		capacityChart.Data[0][0] = float64(measurements[0].CurrentCapacity)
		capacityChart.Data[0][1] = float64(measurements[0].CurrentCapacity)
	} else {
		batteryChart.Data[0] = make([]float64, len(measurements))
		capacityChart.Data[0] = make([]float64, len(measurements))
		for i, m := range measurements {
			batteryChart.Data[0][i] = float64(m.Percentage)
			capacityChart.Data[0][i] = float64(m.CurrentCapacity)
		}
	}
}

// showDashboard отображает интерактивный дашборд в терминале
func showDashboard(db *sqlx.DB, ctx context.Context) error {
	if err := ui.Init(); err != nil {
		return fmt.Errorf("инициализация UI: %w", err)
	}
	defer ui.Close()

	// Получаем данные за последние 50 измерений
	measurements, err := getLastNMeasurements(db, 50)
	if err != nil {
		return fmt.Errorf("получение данных: %w", err)
	}

	if len(measurements) == 0 {
		// Если данных нет, показываем заглушку и ждем первых данных
		placeholder := widgets.NewParagraph()
		placeholder.Title = "Сбор данных"
		placeholder.Text = "Ожидание первых измерений батареи...\nДанные появятся через несколько секунд.\n\n⌨️ Горячие клавиши:\n'q'/'й' / Ctrl+C - Выход\n'h'/'р' - Справка\n🌍 Поддержка русской раскладки"
		placeholder.SetRect(0, 0, 80, 10)

		ui.Render(placeholder)

		// Ждем появления данных или выхода
		uiEvents := ui.PollEvents()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case e := <-uiEvents:
				normalizedKey := normalizeKeyInput(e.ID)
				if normalizedKey == "q" || e.ID == "<C-c>" {
					return nil
				}
			case <-ticker.C:
				measurements, err = getLastNMeasurements(db, 50)
				if err == nil && len(measurements) > 0 {
					goto renderDashboard
				}
			}
		}
	}

renderDashboard:
	// Еще раз проверяем, что данные есть (на случай goto)
	if len(measurements) == 0 {
		return fmt.Errorf("нет данных для отображения дашборда")
	}

	// График заряда батареи
	batteryChart := widgets.NewPlot()
	batteryChart.Title = "Заряд батареи (%)"
	batteryChart.Data = make([][]float64, 1)

	// График емкости
	capacityChart := widgets.NewPlot()
	capacityChart.Title = "Текущая емкость (мАч)"
	capacityChart.Data = make([][]float64, 1)

	// Безопасно инициализируем данные графиков
	safeUpdateChartData(batteryChart, capacityChart, measurements)

	// Стили графиков
	batteryChart.AxesColor = ui.ColorWhite
	batteryChart.LineColors[0] = ui.ColorGreen
	capacityChart.AxesColor = ui.ColorWhite
	capacityChart.LineColors[0] = ui.ColorBlue

	// Текущая информация
	latest := measurements[len(measurements)-1]
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)
	robustRate, _ := computeAvgRateRobust(measurements, 10)
	remaining := computeRemainingTime(latest.CurrentCapacity, robustRate)

	// Анализ аномалий для дашборда
	anomalies := detectBatteryAnomalies(measurements)
	healthAnalysis := analyzeBatteryHealth(measurements)

	infoList := widgets.NewList()
	infoList.Title = "Текущее состояние"
	infoRows := []string{
		fmt.Sprintf("🔋 Заряд: %d%%", latest.Percentage),
		fmt.Sprintf("⚡ Состояние: %s", formatStateWithEmoji(latest.State, latest.Percentage)),
		fmt.Sprintf("🔄 Циклы: %d", latest.CycleCount),
		fmt.Sprintf("📉 Износ: %.1f%%", wear),
		fmt.Sprintf("⏱️ Скорость: %.2f мАч/ч", robustRate),
		fmt.Sprintf("⏰ Время: %s", remaining.Truncate(time.Minute)),
	}

	// Добавляем температуру если доступна
	if latest.Temperature > 0 {
		tempEmoji := "🌡️"
		if latest.Temperature > 40 {
			tempEmoji = "🔥"
		} else if latest.Temperature < 20 {
			tempEmoji = "❄️"
		}
		infoRows = append(infoRows, fmt.Sprintf("%sТемпература: %d°C", tempEmoji, latest.Temperature))
	}

	if healthAnalysis != nil {
		if status, ok := healthAnalysis["health_status"].(string); ok {
			score, _ := healthAnalysis["health_score"].(int)
			infoRows = append(infoRows, fmt.Sprintf("Здоровье: %s (%d/100)", status, score))
		}
		if len(anomalies) > 0 {
			infoRows = append(infoRows, fmt.Sprintf("Аномалий: %d", len(anomalies)))
		}
	}

	infoRows = append(infoRows, getDashboardHotkeys()...)
	infoList.Rows = infoRows

	// Гистограмма состояний
	stateGauge := widgets.NewGauge()
	stateGauge.Title = "Заряд батареи"
	stateGauge.Percent = latest.Percentage
	stateGauge.BarColor = ui.ColorGreen
	stateGauge.BorderStyle = ui.NewStyle(ui.ColorWhite) // Явно задаем стиль границ
	if latest.Percentage < 20 {
		stateGauge.BarColor = ui.ColorRed
	} else if latest.Percentage < 50 {
		stateGauge.BarColor = ui.ColorYellow
	}

	// Износ батареи
	wearGauge := widgets.NewGauge()
	wearGauge.Title = "Износ батареи"
	wearGauge.Percent = int(wear)
	wearGauge.BarColor = ui.ColorRed
	wearGauge.BorderStyle = ui.NewStyle(ui.ColorWhite) // Явно задаем стиль границ

	// Таблица последних измерений
	table := widgets.NewTable()
	table.Title = "Последние измерения"
	table.Rows = [][]string{
		{"Время", "Заряд", "Состояние", "Емкость"},
	}
	// Устанавливаем фиксированную ширину колонок для правильного выравнивания
	// Увеличиваем ширину колонок для корректного отображения
	table.ColumnWidths = []int{10, 8, 12, 12}

	// Вычисляем сколько строк поместится в таблице
	// Применяем лейаут сначала чтобы узнать размеры
	layout := calculateLayout()

	// Проверяем, должна ли таблица отображаться (не нулевые размеры)
	if layout.Table.X2 > layout.Table.X1 && layout.Table.Y2 > layout.Table.Y1 {
		// Высота таблицы = Y2 - Y1, минус 3 строки на рамки и заголовок
		tableHeight := layout.Table.Y2 - layout.Table.Y1 - 3
		if tableHeight > 0 {
			maxRows := max(1, min(tableHeight, len(measurements))) // Минимум 1 строка, максимум сколько поместится

			// Добавляем строки начиная с самых последних
			start := max(0, len(measurements)-maxRows)
			for i := start; i < len(measurements); i++ {
				m := measurements[i]
				timeStr := m.Timestamp[11:19] // только время
				table.Rows = append(table.Rows, []string{
					timeStr,
					fmt.Sprintf("%3d%%", m.Percentage), // Фиксированная ширина для процентов
					m.State,
					fmt.Sprintf("%4d мАч", m.CurrentCapacity), // Фиксированная ширина для емкости
				})
			}
		}
	}

	// Применяем адаптивный лейаут
	applyLayout(layout, batteryChart, capacityChart, infoList, stateGauge, wearGauge, table)

	// Функция для создания списка виджетов к отображению
	getVisibleWidgets := func(currentLayout DashboardLayout) []ui.Drawable {
		var widgets []ui.Drawable

		// Основные виджеты (всегда отображаются)
		widgets = append(widgets, batteryChart, infoList)

		// Дополнительные виджеты только если они имеют размеры
		if currentLayout.CapacityChart.X2 > currentLayout.CapacityChart.X1 && currentLayout.CapacityChart.Y2 > currentLayout.CapacityChart.Y1 {
			widgets = append(widgets, capacityChart)
		}
		if currentLayout.StateGauge.X2 > currentLayout.StateGauge.X1 && currentLayout.StateGauge.Y2 > currentLayout.StateGauge.Y1 {
			widgets = append(widgets, stateGauge)
		}
		if currentLayout.WearGauge.X2 > currentLayout.WearGauge.X1 && currentLayout.WearGauge.Y2 > currentLayout.WearGauge.Y1 {
			widgets = append(widgets, wearGauge)
		}
		if currentLayout.Table.X2 > currentLayout.Table.X1 && currentLayout.Table.Y2 > currentLayout.Table.Y1 {
			widgets = append(widgets, table)
		}

		return widgets
	}

	render := func() {
		widgets := getVisibleWidgets(layout)
		ui.Render(widgets...)
	}

	render()

	uiEvents := ui.PollEvents()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-uiEvents:
			// Нормализуем клавишу для поддержки разных раскладок
			normalizedKey := normalizeKeyInput(e.ID)
			switch normalizedKey {
			case "q":
				return nil
			case "<C-c>":
				return nil
			case "<Resize>":
				// Обработка изменения размера терминала
				layout = calculateLayout() // Обновляем переменную layout
				applyLayout(layout, batteryChart, capacityChart, infoList, stateGauge, wearGauge, table)
				ui.Clear()
				render()
			case "r":
				// Обновляем данные
				newMeasurements, err := getLastNMeasurements(db, 50)
				if err == nil && len(newMeasurements) > 0 {
					measurements = newMeasurements
					latest = measurements[len(measurements)-1]

					// Обновляем графики безопасно
					safeUpdateChartData(batteryChart, capacityChart, measurements)

					// Пересчитываем статистику
					wear = computeWear(latest.DesignCapacity, latest.FullChargeCap)
					robustRate, _ := computeAvgRateRobust(measurements, 10)
					remaining = computeRemainingTime(latest.CurrentCapacity, robustRate)

					// Обновляем анализ
					anomalies = detectBatteryAnomalies(measurements)
					healthAnalysis = analyzeBatteryHealth(measurements)

					// Обновляем виджеты
					stateGauge.Percent = latest.Percentage
					wearGauge.Percent = int(wear)

					// Обновляем информационный список
					infoRows := []string{
						fmt.Sprintf("🔋 Заряд: %d%%", latest.Percentage),
						fmt.Sprintf("⚡ Состояние: %s", formatStateWithEmoji(latest.State, latest.Percentage)),
						fmt.Sprintf("🔄 Циклы: %d", latest.CycleCount),
						fmt.Sprintf("📉 Износ: %.1f%%", wear),
						fmt.Sprintf("⏱️ Скорость: %.2f мАч/ч", robustRate),
						fmt.Sprintf("⏰ Время: %s", remaining.Truncate(time.Minute)),
					}

					// Добавляем температуру если доступна
					if latest.Temperature > 0 {
						tempEmoji := "🌡️"
						if latest.Temperature > 40 {
							tempEmoji = "🔥"
						} else if latest.Temperature < 20 {
							tempEmoji = "❄️"
						}
						infoRows = append(infoRows, fmt.Sprintf("%sТемпература: %d°C", tempEmoji, latest.Temperature))
					}

					if healthAnalysis != nil {
						if status, ok := healthAnalysis["health_status"].(string); ok {
							score, _ := healthAnalysis["health_score"].(int)
							infoRows = append(infoRows, fmt.Sprintf("Здоровье: %s (%d/100)", status, score))
						}
						if len(anomalies) > 0 {
							infoRows = append(infoRows, fmt.Sprintf("Аномалий: %d", len(anomalies)))
						}
					}

					infoRows = append(infoRows, getDashboardHotkeys()...)
					infoList.Rows = infoRows

					// Обновляем лейаут на случай изменения размера
					layout = calculateLayout() // Обновляем переменную layout
					applyLayout(layout, batteryChart, capacityChart, infoList, stateGauge, wearGauge, table)

					render()
				}
			case "h":
				// Показываем справку
				helpWidget := widgets.NewParagraph()
				helpWidget.Title = "Справка - BatMon v2.0"
				helpWidget.Text = `🔋 ИНТЕРАКТИВНЫЙ МОНИТОРИНГ БАТАРЕИ

ОПИСАНИЕ ГРАФИКОВ:
• Левый график - процент заряда батареи во времени
• Правый график - текущая ёмкость в мАч во времени
• Таблица - последние 5 измерений с временными метками

ГОРЯЧИЕ КЛАВИШИ:
• 'q'/'й' / Ctrl+C - выход из мониторинга
• 'r'/'к' - принудительное обновление данных  
• 'h'/'р' - показать эту справку (нажмите любую клавишу для возврата)
🌍 Поддерживается русская раскладка клавиатуры

ПОКАЗАТЕЛИ:
• Заряд - текущий процент заряда батареи
• Состояние - режим работы (заряжается/разряжается/подключен)
• Циклы - количество полных циклов заряда-разряда
• Износ - процент износа относительно заводской ёмкости
• Скорость - текущая скорость разряда в мАч/час
• Время - примерное оставшееся время работы

Данные обновляются автоматически каждые 10 секунд.
Нажмите любую клавишу для возврата к мониторингу...`

				// Устанавливаем размер на весь экран
				termWidth, termHeight := ui.TerminalDimensions()
				helpWidget.SetRect(0, 0, termWidth, termHeight)

				ui.Clear()
				ui.Render(helpWidget)

				// Ждем нажатия любой клавиши
				for {
					helpEvent := <-uiEvents
					if helpEvent.Type == ui.KeyboardEvent {
						ui.Clear()
						render() // Возвращаем обычный дашборд
						break
					}
				}
			}
		case <-ticker.C:
			// Автоматическое обновление каждые 10 секунд
			newMeasurements, err := getLastNMeasurements(db, 50)
			if err == nil && len(newMeasurements) > 0 {
				measurements = newMeasurements
				latest = measurements[len(measurements)-1]
				wear = computeWear(latest.DesignCapacity, latest.FullChargeCap)
				robustRate, _ := computeAvgRateRobust(measurements, 10)
				remaining = computeRemainingTime(latest.CurrentCapacity, robustRate)

				// Обновляем все виджеты безопасно
				safeUpdateChartData(batteryChart, capacityChart, measurements)

				stateGauge.Percent = latest.Percentage
				if latest.Percentage < 20 {
					stateGauge.BarColor = ui.ColorRed
				} else if latest.Percentage < 50 {
					stateGauge.BarColor = ui.ColorYellow
				} else {
					stateGauge.BarColor = ui.ColorGreen
				}

				wearGauge.Percent = int(wear)

				// Обновляем анализ
				anomalies := detectBatteryAnomalies(measurements)
				healthAnalysis := analyzeBatteryHealth(measurements)

				// Обновляем информационный список
				infoRows := []string{
					fmt.Sprintf("🔋 Заряд: %d%%", latest.Percentage),
					fmt.Sprintf("⚡ Состояние: %s", formatStateWithEmoji(latest.State, latest.Percentage)),
					fmt.Sprintf("🔄 Циклы: %d", latest.CycleCount),
					fmt.Sprintf("📉 Износ: %.1f%%", wear),
					fmt.Sprintf("⏱️ Скорость: %.2f мАч/ч", robustRate),
					fmt.Sprintf("⏰ Время: %s", remaining.Truncate(time.Minute)),
				}

				// Добавляем температуру если доступна
				if latest.Temperature > 0 {
					tempEmoji := "🌡️"
					if latest.Temperature > 40 {
						tempEmoji = "🔥"
					} else if latest.Temperature < 20 {
						tempEmoji = "❄️"
					}
					infoRows = append(infoRows, fmt.Sprintf("%sТемпература: %d°C", tempEmoji, latest.Temperature))
				}

				if healthAnalysis != nil {
					if status, ok := healthAnalysis["health_status"].(string); ok {
						score, _ := healthAnalysis["health_score"].(int)
						infoRows = append(infoRows, fmt.Sprintf("Здоровье: %s (%d/100)", status, score))
					}
					if len(anomalies) > 0 {
						infoRows = append(infoRows, fmt.Sprintf("Аномалий: %d", len(anomalies)))
					}
				}

				infoRows = append(infoRows, getDashboardHotkeys()...)
				infoList.Rows = infoRows // Обновляем таблицу последних измерений
				table.Rows = [][]string{
					{"Время", "Заряд", "Состояние", "Емкость"},
				}
				for i := len(measurements) - 5; i < len(measurements) && i >= 0; i++ {
					if i < 0 {
						continue
					}
					m := measurements[i]
					timeStr := m.Timestamp[11:19]
					table.Rows = append(table.Rows, []string{
						timeStr,
						fmt.Sprintf("%d%%", m.Percentage),
						m.State,
						fmt.Sprintf("%d мАч", m.CurrentCapacity),
					})
				}

				// Обновляем лейаут на случай изменения размера
				layout = calculateLayout() // Обновляем переменную layout
				applyLayout(layout, batteryChart, capacityChart, infoList, stateGauge, wearGauge, table)

				render()
			}
		}
	}
}

// printReport выводит отчёт о последнем измерении и статистике с цветным оформлением.
func printReport(db *sqlx.DB) error {
	ms, err := getLastNMeasurements(db, 20) // Увеличиваем количество для лучшего анализа
	if err != nil {
		return fmt.Errorf("получение исторических данных: %w", err)
	}
	if len(ms) == 0 {
		color.Yellow("Нет записей для отчёта.")
		return nil
	}

	latest := ms[len(ms)-1]
	avgRate := computeAvgRate(ms, 5)
	robustRate, validIntervals := computeAvgRateRobust(ms, 10)
	remaining := computeRemainingTime(latest.CurrentCapacity, robustRate)
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)

	// Анализ здоровья батареи
	healthAnalysis := analyzeBatteryHealth(ms)

	// Определяем уровень для цветового оформления
	healthScore := 70
	if healthAnalysis != nil {
		if score, ok := healthAnalysis["health_score"].(int); ok {
			healthScore = score
		}
	}
	statusLevel := getStatusLevel(wear, latest.Percentage, latest.Temperature, healthScore)

	// Краткое резюме
	color.Cyan("💼 === КРАТКОЕ РЕЗЮМЕ ===")
	if healthAnalysis != nil {
		if status, ok := healthAnalysis["health_status"].(string); ok {
			score, _ := healthAnalysis["health_score"].(int)
			printColoredStatus("Здоровье батареи", fmt.Sprintf("%s (рейтинг %d/100)", status, score), getStatusLevel(wear, 100, 25, score))
		}
	}
	printColoredStatus("Циклы", fmt.Sprintf("%d", latest.CycleCount), statusLevel)
	printColoredStatus("Износ", fmt.Sprintf("%.1f%%", wear), getStatusLevel(wear, 100, 25, 100))
	if remaining > 0 {
		printColoredStatus("Оставшееся время", remaining.Truncate(time.Minute).String(), statusLevel)
	}
	fmt.Println()

	color.Cyan("=== Текущее состояние батареи ===")
	localTime, _ := time.Parse(time.RFC3339, latest.Timestamp)
	fmt.Printf("📅 %s | ", localTime.Format("15:04:05 02.01.2006"))
	printColoredStatus("Заряд", fmt.Sprintf("%d%%", latest.Percentage), getStatusLevel(0, latest.Percentage, 25, 100))
	fmt.Printf("⚡ %s\n", formatStateWithEmoji(latest.State, latest.Percentage))
	fmt.Printf("🔄 Кол-во циклов: %d\n", latest.CycleCount)
	fmt.Printf("⚡ Полная ёмкость: %d мАч\n", latest.FullChargeCap)
	fmt.Printf("📐 Проектная ёмкость: %d мАч\n", latest.DesignCapacity)
	fmt.Printf("🔋 Текущая ёмкость: %d мАч\n", latest.CurrentCapacity)

	// Выводим температуру если доступна
	if latest.Temperature > 0 {
		tempLevel := "info"
		if latest.Temperature > 40 {
			tempLevel = "critical"
		} else if latest.Temperature > 35 {
			tempLevel = "warning"
		}
		printColoredStatus("🌡️ Температура", fmt.Sprintf("%d°C", latest.Temperature), tempLevel)
	}

	fmt.Println()
	color.Cyan("=== Анализ здоровья батареи ===")
	if healthAnalysis != nil {
		if status, ok := healthAnalysis["health_status"].(string); ok {
			score, _ := healthAnalysis["health_score"].(int)
			printColoredStatus("Общее состояние", fmt.Sprintf("%s (оценка: %d/100)", status, score), getStatusLevel(wear, 100, 25, score))
		}
		printColoredStatus("Износ батареи", fmt.Sprintf("%.1f%%", wear), getStatusLevel(wear, 100, 25, 100))

		// Анализ трендов
		if trendAnalysis, ok := healthAnalysis["trend_analysis"].(TrendAnalysis); ok {
			if trendAnalysis.DegradationRate != 0 {
				trendLevel := "good"
				if !trendAnalysis.IsHealthy {
					trendLevel = "warning"
				}
				if trendAnalysis.DegradationRate < -1.0 {
					trendLevel = "critical"
				}
				printColoredStatus("📈 Тренд деградации", fmt.Sprintf("%.2f%% в месяц", trendAnalysis.DegradationRate), trendLevel)

				if trendAnalysis.ProjectedLifetime > 0 {
					fmt.Printf("🔮 Прогноз до 80%% емкости: ~%d дней\n", trendAnalysis.ProjectedLifetime)
				}
			}
		}

		if anomalies, ok := healthAnalysis["anomalies"].([]string); ok && len(anomalies) > 0 {
			color.Yellow("\n⚠️  Обнаружено аномалий за последние измерения: %d", len(anomalies))
			for i, anomaly := range anomalies {
				if i >= 5 { // Показываем максимум 5 последних аномалий
					color.Yellow("... и еще %d", len(anomalies)-i)
					break
				}
				color.Red("  • %s", anomaly)
			}
		}

		if recs, ok := healthAnalysis["recommendations"].([]string); ok && len(recs) > 0 {
			color.Green("\n💡 Рекомендации:")
			for _, rec := range recs {
				color.Green("  • %s", rec)
			}
		}
	}

	fmt.Println()
	color.Cyan("=== Статистика разрядки ===")
	if avgRate > 0 {
		fmt.Printf("📊 Простая скорость разрядки: %.2f мАч/час\n", avgRate)
	}
	if robustRate > 0 {
		rateLevel := "good"
		if robustRate > 1000 {
			rateLevel = "warning"
		} else if robustRate > 1500 {
			rateLevel = "critical"
		}
		printColoredStatus("📈 Робастная скорость разрядки", fmt.Sprintf("%.2f мАч/час (на основе %d валидных интервалов)", robustRate, validIntervals), rateLevel)
	} else {
		color.Yellow("📈 Робастная скорость разрядки: недостаточно данных")
	}
	if remaining > 0 {
		printColoredStatus("⏰ Оставшееся время работы", remaining.Truncate(time.Minute).String(), statusLevel)
	} else {
		color.Yellow("⏰ Оставшееся время работы: неизвестно")
	}

	fmt.Println()
	color.Cyan("=== Последние измерения (от старых к новым) ===")
	startIdx := 0
	if len(ms) > 10 {
		startIdx = len(ms) - 10 // Показываем последние 10
	}

	fmt.Printf("%-10s | %-5s | %-12s | %-4s | %-4s | %-4s | %-6s | %-4s\n",
		"Время", "Заряд", "Состояние", "Цикл", "ПЕ", "ПроЕ", "ТекЕ", "Темп")
	fmt.Println(strings.Repeat("-", 80))

	for i := startIdx; i < len(ms); i++ {
		if i < 0 {
			continue
		}
		m := ms[i]
		// Помечаем подозрительные измерения
		marker := "  "
		if i > 0 {
			prev := ms[i-1]
			chargeDiff := abs(m.Percentage - prev.Percentage)
			capacityDiff := abs(m.CurrentCapacity - prev.CurrentCapacity)
			if chargeDiff > 20 || capacityDiff > 500 {
				marker = "⚠️ "
			}
		}

		timeStr := m.Timestamp[11:19] // только время
		tempStr := "-"
		if m.Temperature > 0 {
			tempStr = fmt.Sprintf("%d°C", m.Temperature)
		}

		line := fmt.Sprintf("%s%-10s | %-5d | %-12s | %-4d | %-4d | %-4d | %-6d | %-4s",
			marker, timeStr, m.Percentage,
			strings.Replace(formatStateWithEmoji(m.State, m.Percentage), "🔋", "", -1)[:min(12, len(strings.Replace(formatStateWithEmoji(m.State, m.Percentage), "🔋", "", -1)))],
			m.CycleCount, m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity, tempStr)

		if marker == "⚠️ " {
			color.Red(line)
		} else {
			fmt.Println(line)
		}
	}
	return nil
}

// main – точка входа программы.
func main() {
	// Проверяем аргументы командной строки для обратной совместимости
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-help", "--help", "help":
			showHelp()
			return
		case "-export-md", "--export-md":
			if len(os.Args) < 3 {
				color.New(color.FgRed).Println("❌ Укажите имя файла для экспорта")
				return
			}
			if err := runExportMode(os.Args[2], "", true); err != nil {
				log.Fatalf("❌ Ошибка экспорта: %v", err)
			}
			return
		case "-export-html", "--export-html":
			if len(os.Args) < 3 {
				color.New(color.FgRed).Println("❌ Укажите имя файла для экспорта")
				return
			}
			if err := runExportMode("", os.Args[2], true); err != nil {
				log.Fatalf("❌ Ошибка экспорта: %v", err)
			}
			return
		}
	}

	// Основной интерактивный режим
	for {
		if err := showMainMenu(); err != nil {
			color.New(color.FgRed).Printf("❌ Ошибка: %v\n", err)
			color.New(color.FgWhite).Print("Нажмите Enter для продолжения...")
			fmt.Scanln()
			continue
		}
		break
	}
}

// showMainMenu отображает главное меню и обрабатывает выбор пользователя
func showMainMenu() error {
	for {
		// Очищаем экран и показываем заголовок
		fmt.Print("\033[2J\033[H") // Очистка экрана

		color.New(color.FgCyan, color.Bold).Println("🔋 BatMon v2.0 - Мониторинг батареи MacBook")
		color.New(color.FgWhite).Println("═══════════════════════════════════════════════════════")
		fmt.Println()

		// Показываем текущее состояние батареи
		if err := showQuickStatus(); err != nil {
			color.New(color.FgYellow).Printf("⚠️ Не удалось получить текущий статус: %v\n\n", err)
		}

		// Главное меню
		color.New(color.FgGreen, color.Bold).Println("📋 Выберите действие:")
		fmt.Println()
		fmt.Println("  1️⃣  Запустить интерактивный мониторинг")
		fmt.Println("  2️⃣  Показать детальный отчет")
		fmt.Println("  3️⃣  Экспортировать отчеты")
		fmt.Println("  4️⃣  Статистика и настройки")
		fmt.Println("  5️⃣  Справка")
		fmt.Println("  0️⃣  Выход")
		fmt.Println()

		color.New(color.FgWhite).Print("Ваш выбор (0-5): ")

		var choice string
		fmt.Scanln(&choice)

		switch choice {
		case "1":
			return runMonitoringMode()
		case "2":
			return runReportMode()
		case "3":
			return runExportMenu()
		case "4":
			return runSettingsMenu()
		case "5":
			showHelp()
		case "0", "q", "exit":
			color.New(color.FgGreen).Println("\n👋 До свидания!")
			return nil
		default:
			color.New(color.FgRed).Println("\n❌ Неверный выбор. Нажмите Enter для продолжения...")
			fmt.Scanln()
		}
	}
}

// showQuickStatus показывает краткий статус батареи
func showQuickStatus() error {
	pct, state, err := parsePMSet()
	if err != nil {
		return fmt.Errorf("получение статуса: %w", err)
	}

	// Определяем цвет для процента заряда
	var percentColor *color.Color
	if pct >= 50 {
		percentColor = color.New(color.FgGreen, color.Bold)
	} else if pct >= 20 {
		percentColor = color.New(color.FgYellow, color.Bold)
	} else {
		percentColor = color.New(color.FgRed, color.Bold)
	}

	// Форматируем статус
	stateFormatted := formatStateWithEmoji(state, pct)

	color.New(color.FgWhite).Print("💡 Текущий статус: ")
	percentColor.Printf("%d%% ", pct)
	color.New(color.FgCyan).Printf("(%s)", stateFormatted)

	// Добавляем информацию о режиме питания
	if strings.ToLower(state) == "charging" {
		color.New(color.FgBlue).Print(" 🔌 На зарядке")
	} else if strings.ToLower(state) == "discharging" {
		color.New(color.FgMagenta).Print(" 🔋 От батареи")
	} else {
		color.New(color.FgGreen).Print(" ✅ Заряжена")
	}

	fmt.Println()
	fmt.Println()

	return nil
}

// runMonitoringMode запускает интерактивный мониторинг
func runMonitoringMode() error {
	color.New(color.FgGreen).Println("🔄 Запуск интерактивного мониторинга...")
	fmt.Println("💡 Программа определит режим работы автоматически")
	fmt.Println()

	// Инициализируем БД
	db, err := initDB(dbFile)
	if err != nil {
		return fmt.Errorf("инициализация БД: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Обработка сигналов
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		color.New(color.FgYellow).Println("\n⏹️ Получен сигнал завершения...")
		cancel()
	}()

	// Проверяем состояние питания
	onBattery, state, percentage, err := isOnBattery()
	if err != nil {
		color.New(color.FgYellow).Printf("⚠️ Ошибка определения питания: %v\n", err)
		return runReportMode() // Показываем отчет по имеющимся данным
	}

	color.New(color.FgCyan).Printf("⚡ Состояние питания: %s (%d%%)\n",
		formatStateWithEmoji(state, percentage), percentage)

	if onBattery {
		color.New(color.FgBlue).Println("🔋 Работа от батареи - запуск мониторинга и дашборда...")

		// Запускаем сбор данных в фоне
		var wg sync.WaitGroup
		wg.Add(1)
		go backgroundDataCollection(db, ctx, &wg)

		// Небольшая задержка для первого измерения
		time.Sleep(2 * time.Second)

		// Показываем дашборд
		if err := showDashboard(db, ctx); err != nil {
			log.Printf("дашборд: %v", err)
		}

		cancel()
		wg.Wait()
	} else {
		color.New(color.FgGreen).Println("🔌 Работа от сети - показ сохраненных данных...")
		return runReportMode()
	}

	return nil
}

// runReportMode показывает детальный отчет
func runReportMode() error {
	color.New(color.FgBlue).Println("📊 Загрузка детального отчета...")

	db, err := initDB(dbFile)
	if err != nil {
		return fmt.Errorf("инициализация БД: %w", err)
	}
	defer db.Close()

	if err := printReport(db); err != nil {
		return fmt.Errorf("вывод отчёта: %w", err)
	}

	color.New(color.FgWhite).Print("\nНажмите Enter для возврата в меню...")
	fmt.Scanln()

	return nil
}

// runExportMenu показывает меню экспорта
func runExportMenu() error {
	for {
		fmt.Print("\033[2J\033[H") // Очистка экрана

		color.New(color.FgCyan, color.Bold).Println("📄 Экспорт отчетов")
		color.New(color.FgWhite).Println("═══════════════════════════════")
		fmt.Println()

		fmt.Println("  1️⃣  Экспорт в Markdown (.md)")
		fmt.Println("  2️⃣  Экспорт в HTML (.html)")
		fmt.Println("  3️⃣  Экспорт в оба формата")
		fmt.Println("  0️⃣  Назад в главное меню")
		fmt.Println()

		color.New(color.FgWhite).Print("Выберите формат (0-3): ")

		var choice string
		fmt.Scanln(&choice)

		switch choice {
		case "1":
			return handleExport("md")
		case "2":
			return handleExport("html")
		case "3":
			return handleExport("both")
		case "0", "back":
			return nil
		default:
			color.New(color.FgRed).Println("\n❌ Неверный выбор. Нажмите Enter для продолжения...")
			fmt.Scanln()
		}
	}
}

// handleExport обрабатывает экспорт в выбранном формате
func handleExport(format string) error {
	color.New(color.FgWhite).Print("📝 Введите имя файла (без расширения): ")
	var filename string
	fmt.Scanln(&filename)

	if filename == "" {
		filename = fmt.Sprintf("battery_report_%s", time.Now().Format("20060102_150405"))
		color.New(color.FgCyan).Printf("💡 Используется имя по умолчанию: %s\n", filename)
	}

	var markdownFile, htmlFile string

	switch format {
	case "md":
		markdownFile = filename
	case "html":
		htmlFile = filename
	case "both":
		markdownFile = filename
		htmlFile = filename
	}

	fmt.Println()
	color.New(color.FgBlue).Println("📊 Генерация отчета...")

	err := runExportMode(markdownFile, htmlFile, false)
	if err != nil {
		color.New(color.FgRed).Printf("❌ Ошибка экспорта: %v\n", err)
	} else {
		color.New(color.FgGreen).Println("✅ Экспорт выполнен успешно!")
	}

	color.New(color.FgWhite).Print("\nНажмите Enter для продолжения...")
	fmt.Scanln()

	return err
}

// runSettingsMenu показывает меню настроек и статистики
func runSettingsMenu() error {
	for {
		fmt.Print("\033[2J\033[H") // Очистка экрана

		color.New(color.FgCyan, color.Bold).Println("⚙️ Статистика и настройки")
		color.New(color.FgWhite).Println("═══════════════════════════════")
		fmt.Println()

		// Показываем статистику БД
		if err := showDatabaseStats(); err != nil {
			color.New(color.FgRed).Printf("❌ Ошибка получения статистики: %v\n", err)
		}

		fmt.Println()
		fmt.Println("  1️⃣  Показать расширенные метрики")
		fmt.Println("  2️⃣  Очистить старые данные")
		fmt.Println("  3️⃣  Информация о системе")
		fmt.Println("  0️⃣  Назад в главное меню")
		fmt.Println()

		color.New(color.FgWhite).Print("Ваш выбор (0-3): ")

		var choice string
		fmt.Scanln(&choice)

		switch choice {
		case "1":
			return showAdvancedMetrics()
		case "2":
			return cleanupOldData()
		case "3":
			return showSystemInfo()
		case "0", "back":
			return nil
		default:
			color.New(color.FgRed).Println("\n❌ Неверный выбор. Нажмите Enter для продолжения...")
			fmt.Scanln()
		}
	}
}

// showDatabaseStats показывает статистику базы данных
func showDatabaseStats() error {
	db, err := initDB(dbFile)
	if err != nil {
		return err
	}
	defer db.Close()

	collector := NewDataCollector(db)
	stats, err := collector.GetStats()
	if err != nil {
		return err
	}

	color.New(color.FgGreen).Println("📊 Статистика данных:")
	fmt.Printf("   📦 Записей в БД: %v\n", stats["total_records"])
	fmt.Printf("   💾 Размер БД: %.1f МБ\n", stats["db_size_mb"])
	fmt.Printf("   🗄️ Буфер памяти: %v/%v записей\n", stats["buffer_size"], stats["buffer_max_size"])

	if oldest, ok := stats["oldest_record"].(string); ok && oldest != "" {
		color.New(color.FgCyan).Printf("   📅 Самая старая запись: %s\n", oldest)
	}
	if newest, ok := stats["newest_record"].(string); ok && newest != "" {
		color.New(color.FgCyan).Printf("   📅 Самая новая запись: %s\n", newest)
	}

	return nil
}

// showAdvancedMetrics показывает расширенные метрики
func showAdvancedMetrics() error {
	color.New(color.FgBlue).Println("🔬 Загрузка расширенных метрик...")

	db, err := initDB(dbFile)
	if err != nil {
		return err
	}
	defer db.Close()

	measurements, err := getLastNMeasurements(db, 50)
	if err != nil {
		return fmt.Errorf("получение данных: %w", err)
	}

	if len(measurements) == 0 {
		color.New(color.FgYellow).Println("⚠️ Недостаточно данных для анализа")
		color.New(color.FgWhite).Print("Нажмите Enter для продолжения...")
		fmt.Scanln()
		return nil
	}

	metrics := analyzeAdvancedMetrics(measurements)

	fmt.Println()
	color.New(color.FgGreen, color.Bold).Println("🔬 Расширенные метрики:")
	color.New(color.FgWhite).Println("═══════════════════════════════")

	fmt.Printf("⚡ Энергоэффективность: %.1f%%\n", metrics.PowerEfficiency)
	fmt.Printf("🔧 Стабильность напряжения: %.1f%%\n", metrics.VoltageStability)
	fmt.Printf("🔋 Эффективность зарядки: %.2f\n", metrics.ChargingEfficiency)
	fmt.Printf("📊 Тренд мощности: %s\n", metrics.PowerTrend)
	fmt.Printf("🏆 Рейтинг здоровья: %d/100\n", metrics.HealthRating)
	fmt.Printf("🍎 Статус Apple: %s\n", metrics.AppleStatus)

	fmt.Println()
	color.New(color.FgWhite).Print("Нажмите Enter для продолжения...")
	fmt.Scanln()

	return nil
}

// cleanupOldData выполняет очистку старых данных
func cleanupOldData() error {
	color.New(color.FgYellow).Println("🧹 Очистка старых данных...")

	db, err := initDB(dbFile)
	if err != nil {
		return err
	}
	defer db.Close()

	retention := NewDataRetention(db, 90*24*time.Hour) // 3 месяца

	if err := retention.Cleanup(); err != nil {
		color.New(color.FgRed).Printf("❌ Ошибка очистки: %v\n", err)
	} else {
		color.New(color.FgGreen).Println("✅ Очистка выполнена успешно")
	}

	color.New(color.FgWhite).Print("Нажмите Enter для продолжения...")
	fmt.Scanln()

	return nil
}

// showSystemInfo показывает информацию о системе
func showSystemInfo() error {
	color.New(color.FgGreen, color.Bold).Println("💻 Информация о системе:")
	color.New(color.FgWhite).Println("═══════════════════════════════")

	// Информация о версии Go
	fmt.Printf("🔧 Версия Go: %s\n", "1.24+")
	fmt.Printf("💾 База данных: SQLite с WAL режимом\n")
	fmt.Printf("📁 Файл БД: %s\n", dbFile)

	// Проверяем доступность команд
	if _, err := exec.LookPath("pmset"); err == nil {
		color.New(color.FgGreen).Println("✅ pmset доступен")
	} else {
		color.New(color.FgRed).Println("❌ pmset недоступен")
	}

	if _, err := exec.LookPath("system_profiler"); err == nil {
		color.New(color.FgGreen).Println("✅ system_profiler доступен")
	} else {
		color.New(color.FgRed).Println("❌ system_profiler недоступен")
	}

	fmt.Println()
	color.New(color.FgWhite).Print("Нажмите Enter для продолжения...")
	fmt.Scanln()

	return nil
}

// showHelp показывает справочную информацию
func showHelp() {
	fmt.Print("\033[2J\033[H") // Очистка экрана

	color.New(color.FgCyan, color.Bold).Println("❓ Справка BatMon v2.0")
	color.New(color.FgWhite).Println("═══════════════════════════════")
	fmt.Println()

	color.New(color.FgGreen).Println("🔋 О программе:")
	fmt.Println("BatMon - это продвинутая утилита для мониторинга состояния батареи MacBook.")
	fmt.Println("Поддерживает интерактивный мониторинг, детальную аналитику и экспорт отчетов.")
	fmt.Println()

	color.New(color.FgYellow).Println("📊 Возможности:")
	fmt.Println("• Интерактивный дашборд с графиками")
	fmt.Println("• Анализ трендов и прогноз деградации")
	fmt.Println("• Мониторинг температуры и расширенных метрик")
	fmt.Println("• Экспорт в Markdown и HTML форматы")
	fmt.Println("• Автоматическая ретенция данных")
	fmt.Println("• Цветной вывод и эмодзи индикаторы")
	fmt.Println()

	color.New(color.FgBlue).Println("🎯 Режимы работы:")
	fmt.Println("1. Интерактивный мониторинг - при работе от батареи")
	fmt.Println("2. Детальный отчет - анализ сохраненных данных")
	fmt.Println("3. Экспорт отчетов - сохранение в файлы")
	fmt.Println("4. Статистика - информация о данных и системе")
	fmt.Println()

	color.New(color.FgMagenta).Println("🔧 Требования:")
	fmt.Println("• macOS (протестировано на Apple Silicon)")
	fmt.Println("• Go 1.24+ для сборки из исходников")
	fmt.Println("• MacBook с батареей")
	fmt.Println()

	color.New(color.FgRed).Println("🆘 Поддержка:")
	fmt.Println("• GitHub: https://github.com/region23/batmon")
	fmt.Println("• Issues: сообщайте о проблемах через GitHub Issues")
	fmt.Println()

	color.New(color.FgWhite).Print("Нажмите Enter для возврата в меню...")
	fmt.Scanln()
}

// runExportMode выполняет экспорт отчетов
func runExportMode(markdownFile, htmlFile string, quiet bool) error {
	if !quiet {
		fmt.Println("🔋 Batmon - Экспорт отчетов")
	}

	db, err := initDB(dbFile)
	if err != nil {
		return fmt.Errorf("инициализация БД: %w", err)
	}
	defer db.Close()

	// Генерируем данные для отчета
	data, err := generateReportData(db)
	if err != nil {
		return fmt.Errorf("генерация данных отчета: %w", err)
	}

	var exported []string

	// Экспорт в Markdown
	if markdownFile != "" {
		if !strings.HasSuffix(markdownFile, ".md") {
			markdownFile += ".md"
		}

		if !quiet {
			fmt.Printf("📝 Экспортирую отчет в Markdown: %s\n", markdownFile)
		}

		if err := exportToMarkdown(data, markdownFile); err != nil {
			return fmt.Errorf("экспорт в Markdown: %w", err)
		}
		exported = append(exported, markdownFile)
	}

	// Экспорт в HTML
	if htmlFile != "" {
		if !strings.HasSuffix(htmlFile, ".html") && !strings.HasSuffix(htmlFile, ".htm") {
			htmlFile += ".html"
		}

		if !quiet {
			fmt.Printf("🌐 Экспортирую отчет в HTML: %s\n", htmlFile)
		}

		if err := exportToHTML(data, htmlFile); err != nil {
			return fmt.Errorf("экспорт в HTML: %w", err)
		}
		exported = append(exported, htmlFile)
	}

	if !quiet && len(exported) > 0 {
		fmt.Printf("✅ Экспорт завершен! Созданы файлы:\n")
		for _, file := range exported {
			absPath, _ := filepath.Abs(file)
			fmt.Printf("   - %s\n", absPath)
		}
	}

	return nil
}
