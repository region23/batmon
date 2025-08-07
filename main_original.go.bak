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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

const (
	pmsetInterval    = 30 * time.Second // интервал опроса pmset
	profilerInterval = 2 * time.Minute  // интервал опроса system_profiler
)

// getDataDir возвращает кроссплатформенную папку для данных приложения по стандарту XDG
func getDataDir() (string, error) {
	var dataDir string
	
	// Определяем папку в зависимости от ОС следуя XDG Base Directory Specification
	switch runtime.GOOS {
	case "windows":
		// Windows: %LOCALAPPDATA%\batmon (или %APPDATA%\batmon)
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			dataDir = filepath.Join(localAppData, "batmon")
		} else if appData := os.Getenv("APPDATA"); appData != "" {
			dataDir = filepath.Join(appData, "batmon")
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
			}
			dataDir = filepath.Join(homeDir, "AppData", "Local", "batmon")
		}
		
	case "darwin":
		// macOS: ~/.local/share/batmon (XDG-совместимо, как на Linux)
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
		}
		// Используем XDG_DATA_HOME или ~/.local/share
		if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
			dataDir = filepath.Join(xdgDataHome, "batmon")
		} else {
			dataDir = filepath.Join(homeDir, ".local", "share", "batmon")
		}
		
	default:
		// Linux и другие Unix: ~/.local/share/batmon (XDG Base Directory)
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
		}
		
		// Используем XDG_DATA_HOME если установлена, иначе ~/.local/share
		if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
			dataDir = filepath.Join(xdgDataHome, "batmon")
		} else {
			dataDir = filepath.Join(homeDir, ".local", "share", "batmon")
		}
	}
	
	// Создаем папку если её нет
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("не удалось создать папку для данных: %w", err)
	}
	
	return dataDir, nil
}

// getDBPath возвращает путь к файлу базы данных
func getDBPath() string {
	dataDir, err := getDataDir()
	if err != nil {
		// Fallback на текущую директорию если не можем создать папку данных
		log.Printf("Не удалось создать папку данных, используем текущую папку: %v", err)
		return "batmon.sqlite"
	}
	
	return filepath.Join(dataDir, "batmon.sqlite")
}

// getDocumentsDir возвращает путь к папке Documents пользователя
func getDocumentsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
	}
	
	documentsDir := filepath.Join(homeDir, "Documents")
	return documentsDir, nil
}

// getExportPath возвращает полный путь для экспортируемого файла
func getExportPath(filename string) (string, error) {
	// Если путь уже абсолютный, используем как есть
	if filepath.IsAbs(filename) {
		return filename, nil
	}
	
	// Если содержит разделители пути, используем как есть (относительный путь)
	if strings.Contains(filename, string(filepath.Separator)) {
		return filename, nil
	}
	
	// Иначе сохраняем в Documents
	documentsDir, err := getDocumentsDir()
	if err != nil {
		// Fallback на текущую директорию
		return filename, nil
	}
	
	return filepath.Join(documentsDir, filename), nil
}

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
	if dbFileInfo, err := os.Stat(getDBPath()); err == nil {
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

// Bubble Tea приложение типы
type AppState int

const (
	StateWelcome AppState = iota
	StateMenu
	StateDashboard
	StateReport
	StateQuickDiag
	StateExport
	StateSettings
	StateHelp
)

// App - основная модель приложения Bubble Tea
type App struct {
	state        AppState
	windowWidth  int
	windowHeight int
	
	// Компоненты
	menu       MenuModel
	dashboard  DashboardModel
	report     ReportModel
	
	// Сервисы
	dataService *DataService
	
	// Общие данные
	measurements []Measurement
	latest       *Measurement
	
	// Экспорт
	exportStatus string
	
	// Скроллинг отчета
	reportScrollY int
	
	// Скроллинг dashboard
	dashboardScrollY int
	
	// Ошибки
	lastError error
}

// MenuModel - модель главного меню
type MenuModel struct {
	list   list.Model
	choice string
}

// DashboardModel - модель интерактивного dashboard
type DashboardModel struct {
	batteryChart  ChartModel
	capacityChart ChartModel
	infoList      InfoListModel
	batteryGauge  progress.Model
	wearGauge     progress.Model
	measureTable  table.Model
	
	lastUpdate time.Time
	updating   bool
}

// ReportModel - модель детального отчета
type ReportModel struct {
	content       string
	scrollY       int
	viewHeight    int
	activeTab     int               // Активная вкладка
	tabs          []string          // Список вкладок
	widgets       []ReportWidget    // Виджеты для отображения
	historyTable  table.Model       // Таблица истории
	filterState   string            // Фильтр для истории
	sortColumn    int               // Колонка для сортировки
	sortDesc      bool              // Направление сортировки
	lastUpdate    time.Time         // Время последнего обновления
	animationTick int               // Счетчик для анимаций
}

// ReportWidget - виджет для отображения в отчете
type ReportWidget struct {
	title   string
	content string
	widgetType string // "gauge", "chart", "info", "alert"
	value   float64
	maxValue float64
	color   lipgloss.Color
	icon    string
}

// ChartModel - кастомная модель для ASCII графиков (заменено на charts.go)  
type ChartModel struct {
	title string
	data  []float64
}

// InfoListModel - модель информационного списка
type InfoListModel struct {
	items []InfoItem
}

type InfoItem struct {
	label string
	value string
	icon  string
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

// menuItem реализует list.Item интерфейс
type menuItem struct {
	title string
	desc  string
}

func (i menuItem) FilterValue() string { return i.title }
func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }

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
    <script src="https://cdnjs.cloudflare.com/ajax/libs/Chart.js/3.9.1/chart.min.js" integrity="sha512-ElRFoEQdI5Ht6kZvyzXhYG9NqjtkmlkfYk0wr6wHxU9JEHakS7UJZNeml5ALk+8IKlU6jDgMabC3vkumRokgJA==" crossorigin="anonymous" referrerpolicy="no-referrer"></script>
    <script>
        // Fallback: встроенная минимальная версия Chart.js если CDN недоступен
        if (typeof Chart === 'undefined') {
            // Простая замена Chart.js для автономной работы
            window.Chart = function(ctx, config) {
                var canvas = ctx.canvas || ctx;
                var context = canvas.getContext('2d');
                
                // Очищаем canvas
                context.clearRect(0, 0, canvas.width, canvas.height);
                
                if (config.type === 'line' && config.data && config.data.datasets) {
                    var data = config.data.datasets[0].data;
                    var labels = config.data.labels;
                    
                    if (data && data.length > 0) {
                        // Настройки графика
                        var padding = 40;
                        var width = canvas.width - 2 * padding;
                        var height = canvas.height - 2 * padding;
                        
                        // Найдем min и max значения
                        var minVal = Math.min(...data);
                        var maxVal = Math.max(...data);
                        var range = maxVal - minVal;
                        if (range === 0) range = 1;
                        
                        // Рисуем оси
                        context.strokeStyle = '#666';
                        context.lineWidth = 1;
                        context.beginPath();
                        context.moveTo(padding, padding);
                        context.lineTo(padding, height + padding);
                        context.lineTo(width + padding, height + padding);
                        context.stroke();
                        
                        // Рисуем данные
                        if (data.length > 1) {
                            context.strokeStyle = config.data.datasets[0].borderColor || '#007AFF';
                            context.lineWidth = 2;
                            context.beginPath();
                            
                            for (var i = 0; i < data.length; i++) {
                                var x = padding + (i / (data.length - 1)) * width;
                                var y = height + padding - ((data[i] - minVal) / range) * height;
                                
                                if (i === 0) {
                                    context.moveTo(x, y);
                                } else {
                                    context.lineTo(x, y);
                                }
                            }
                            context.stroke();
                        }
                        
                        // Подписи осей
                        context.fillStyle = '#333';
                        context.font = '12px Arial';
                        context.textAlign = 'center';
                        
                        // Y-axis labels
                        context.textAlign = 'right';
                        context.fillText(maxVal.toFixed(0), padding - 10, padding + 5);
                        context.fillText(minVal.toFixed(0), padding - 10, height + padding + 5);
                        
                        // Заголовок
                        if (config.options && config.options.plugins && config.options.plugins.title && config.options.plugins.title.text) {
                            context.textAlign = 'center';
                            context.font = 'bold 16px Arial';
                            context.fillText(config.options.plugins.title.text, canvas.width / 2, 20);
                        }
                    }
                }
                
                return {
                    update: function() {},
                    destroy: function() {}
                };
            };
        }
    </script>
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
	// Проверяем аргументы командной строки для экспорта и справки
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version", "version", "-v":
			showVersion()
			return
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

	// Запуск интерфейса Bubble Tea
	app := NewApp()
	
	// Обработка сигналов для корректного завершения caffeinate
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		if app.dataService != nil {
			app.dataService.Stop()
		}
		os.Exit(0)
	}()
	
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatalf("❌ Ошибка запуска приложения: %v", err)
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
	db, err := initDB(getDBPath())
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

		// Возвращаемся в меню для работы с Bubble Tea
		color.New(color.FgBlue).Println("🔋 Данные собираются в фоне. Используйте главное меню для мониторинга.")
		
		cancel()
		wg.Wait()
		return nil
	} else {
		color.New(color.FgGreen).Println("🔌 Работа от сети - показ сохраненных данных...")
		return runReportMode()
	}
}

// runReportMode показывает детальный отчет
func runReportMode() error {
	color.New(color.FgBlue).Println("📊 Загрузка детального отчета...")

	db, err := initDB(getDBPath())
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

// runSettingsMenu показывает меню очистки БД
func runSettingsMenu() error {
	fmt.Print("\033[2J\033[H") // Очистка экрана

	color.New(color.FgRed, color.Bold).Println("🗑️  Очистка базы данных")
	color.New(color.FgWhite).Println("═══════════════════════════════")
	fmt.Println()
	
	color.New(color.FgYellow, color.Bold).Println("⚠️  ВНИМАНИЕ: Эта операция удалит ВСЕ сохраненные данные!")
	fmt.Println()
	fmt.Println("Будут удалены:")
	fmt.Println("  • Все измерения батареи")
	fmt.Println("  • История состояний")
	fmt.Println("  • Статистика использования")
	fmt.Println()
	
	color.New(color.FgWhite).Print("Вы уверены? (y/н): ")
	
	var choice string
	fmt.Scanln(&choice)
	
	if choice == "y" || choice == "Y" || choice == "н" || choice == "Н" {
		// Удаляем файлы базы данных
		dbPath := getDBPath()
		dbFiles := []string{
			dbPath,                // .batmon.sqlite
			dbPath + "-shm",       // .batmon.sqlite-shm
			dbPath + "-wal",       // .batmon.sqlite-wal
		}
		
		for _, file := range dbFiles {
			if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				// Не возвращаем ошибку, если файл не существует
				color.New(color.FgYellow).Printf("⚠️  Не удалось удалить %s: %v\n", file, err)
			}
		}
		
		color.New(color.FgGreen).Println("✅ База данных успешно очищена!")
		fmt.Println("\nНажмите Enter для продолжения...")
		fmt.Scanln()
	} else {
		color.New(color.FgYellow).Println("❌ Операция отменена")
		fmt.Println("\nНажмите Enter для продолжения...")
		fmt.Scanln()
	}
	
	return nil
}

// showDatabaseStats показывает статистику базы данных
func showDatabaseStats() error {
	db, err := initDB(getDBPath())
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

	db, err := initDB(getDBPath())
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

	db, err := initDB(getDBPath())
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
	fmt.Printf("📁 Файл БД: %s\n", getDBPath())

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

// getVersion возвращает версию приложения из git тега
func getVersion() string {
	cmd := exec.Command("git", "describe", "--tags", "--always", "--dirty")
	output, err := cmd.Output()
	if err != nil {
		// Если git недоступен или нет тегов, возвращаем версию по умолчанию
		return "v2.0-dev"
	}
	
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "v2.0-dev"
	}
	
	return version
}

// showVersion показывает версию приложения
func showVersion() {
	version := getVersion()
	color.New(color.FgCyan, color.Bold).Printf("BatMon %s\n", version)
	color.New(color.FgWhite).Println("Мониторинг батареи MacBook (Apple Silicon)")
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

	color.New(color.FgMagenta).Println("🫧 Интерфейс Bubble Tea (по умолчанию):")
	fmt.Println("Современный интерфейс с:")
	fmt.Println("• Интерактивными компонентами и анимациями")
	fmt.Println("• Отличной отзывчивостью и производительностью")
	fmt.Println("• Адаптивными макетами")
	fmt.Println("• Красивой стилизацией")
	fmt.Println()
	color.New(color.FgCyan).Println("Запуск: ./batmon")
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

	db, err := initDB(getDBPath())
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
		
		// Получаем правильный путь для экспорта
		fullMarkdownPath, err := getExportPath(markdownFile)
		if err != nil {
			return fmt.Errorf("не удалось определить путь для Markdown файла: %w", err)
		}

		if !quiet {
			fmt.Printf("📝 Экспортирую отчет в Markdown: %s\n", fullMarkdownPath)
		}

		if err := exportToMarkdown(data, fullMarkdownPath); err != nil {
			return fmt.Errorf("экспорт в Markdown: %w", err)
		}
		exported = append(exported, fullMarkdownPath)
	}

	// Экспорт в HTML
	if htmlFile != "" {
		if !strings.HasSuffix(htmlFile, ".html") && !strings.HasSuffix(htmlFile, ".htm") {
			htmlFile += ".html"
		}
		
		// Получаем правильный путь для экспорта
		fullHTMLPath, err := getExportPath(htmlFile)
		if err != nil {
			return fmt.Errorf("не удалось определить путь для HTML файла: %w", err)
		}

		if !quiet {
			fmt.Printf("🌐 Экспортирую отчет в HTML: %s\n", fullHTMLPath)
		}

		if err := exportToHTML(data, fullHTMLPath); err != nil {
			return fmt.Errorf("экспорт в HTML: %w", err)
		}
		exported = append(exported, fullHTMLPath)
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

// Bubble Tea функции

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

// Сообщения Bubble Tea
type tickMsg time.Time
type dataUpdateMsg struct {
	measurements []Measurement
	latest       *Measurement
}

type errorMsg struct{ err error }

// Команды Bubble Tea
func tickEvery() tea.Cmd {
	return tea.Every(time.Second*10, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func updateData(ds *DataService) tea.Cmd {
	return func() tea.Msg {
		latest := ds.GetLatest()
		measurements := ds.GetLast(50)
		return dataUpdateMsg{
			measurements: measurements,
			latest:       latest,
		}
	}
}

// NewApp создает новое приложение
func NewApp() *App {
	// Инициализация базы данных и буфера
	db, err := initDB(getDBPath())
	if err != nil {
		log.Fatal(err)
	}
	
	buffer := NewMemoryBuffer(100)
	if err := buffer.LoadFromDB(db, 100); err != nil {
		log.Printf("Предупреждение: не удалось загрузить данные из БД: %v", err)
	}
	
	// Создание сервиса данных
	dataService := NewDataService(db, buffer)
	dataService.Start()
	
	// Создание главного меню
	menuItems := []list.Item{
		menuItem{title: "🔋 Полный анализ батареи (100% → 0%)", desc: "Запустите при 100% заряде, разрядите до 0% для полной диагностики"},
		menuItem{title: "⚡ Быстрая диагностика", desc: "Проверить текущее состояние батареи и показать рекомендации"},
		menuItem{title: "📊 Детальный отчет", desc: "Анализ всех сохраненных данных с графиками и прогнозами"},
		menuItem{title: "📄 Экспорт отчетов", desc: "Сохранить результаты в Markdown или HTML с графиками"},
		menuItem{title: "🗑️  Очистить данные", desc: "Удалить все сохраненные измерения (начать заново)"},
		menuItem{title: "❓ Справка", desc: "Как правильно использовать программу для анализа батареи"},
		menuItem{title: "❌ Выход", desc: "Завершить работу программы"},
	}
	
	menuList := list.New(menuItems, list.NewDefaultDelegate(), 0, 0)
	menuList.Title = "🔋 BatMon - Мониторинг батареи MacBook"
	
	return &App{
		state: StateWelcome,
		menu: MenuModel{
			list: menuList,
		},
		dataService: dataService,
	}
}

// Init инициализирует модель
func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tickEvery(),
		updateData(a.dataService),
	)
}

// Update обрабатывает сообщения
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.windowWidth = msg.Width
		a.windowHeight = msg.Height
		a.updateComponentSizes()
		
	case tea.KeyMsg:
		switch a.state {
		case StateWelcome:
			return a.updateWelcome(msg)
		case StateMenu:
			return a.updateMenu(msg)
		case StateDashboard:
			return a.updateDashboard(msg)
		case StateReport:
			return a.updateReport(msg)
		case StateQuickDiag:
			return a.updateQuickDiag(msg)
		case StateExport:
			return a.updateExport(msg)
		case StateSettings:
			return a.updateSettings(msg)
		case StateHelp:
			return a.updateHelp(msg)
		}
		
	case tickMsg:
		cmds = append(cmds, tickEvery())
		if a.state == StateDashboard {
			cmds = append(cmds, updateData(a.dataService))
		}
		
	case dataUpdateMsg:
		a.measurements = msg.measurements
		a.latest = msg.latest
		if a.state == StateDashboard {
			a.updateDashboardData()
		}
	}
	
	return a, tea.Batch(cmds...)
}

// updateMenu обрабатывает обновления меню
func (a *App) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.dataService.Stop()
		return a, tea.Quit
		
	case "enter":
		selected := a.menu.list.SelectedItem()
		if item, ok := selected.(menuItem); ok {
			switch item.title {
			case "🔋 Полный анализ батареи (100% → 0%)":
				a.state = StateDashboard
				a.initDashboard()
			case "⚡ Быстрая диагностика":
				a.state = StateQuickDiag
				a.initQuickDiag()
			case "📊 Детальный отчет":
				a.state = StateReport
				a.initReport()
			case "📄 Экспорт отчетов":
				a.state = StateExport
			case "🗑️  Очистить данные":
				a.state = StateSettings
			case "❓ Справка":
				a.state = StateHelp
			case "❌ Выход":
				a.dataService.Stop()
				return a, tea.Quit
			}
		}
	}
	
	var cmd tea.Cmd
	a.menu.list, cmd = a.menu.list.Update(msg)
	return a, cmd
}

// updateDashboard обрабатывает обновления dashboard
func (a *App) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.state = StateMenu
		a.dashboardScrollY = 0 // Сбрасываем скролл при выходе
		return a, nil
	case "r", "к":
		return a, updateData(a.dataService)
	case "h", "р":
		// Показать краткую справку (можно расширить позже)
		return a, nil
	case "up", "k", "л":
		// Скролл вверх
		if a.dashboardScrollY > 0 {
			a.dashboardScrollY--
		}
		return a, nil
	case "down", "j", "о":
		// Скролл вниз (максимум определяется в renderDashboard)
		maxScroll := a.calculateMaxDashboardScroll()
		if a.dashboardScrollY < maxScroll {
			a.dashboardScrollY++
		}
		return a, nil
	}
	return a, nil
}

// updateReport обрабатывает обновления отчета
func (a *App) updateReport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.state = StateMenu
		a.reportScrollY = 0 // Сбрасываем скролл при выходе
		return a, nil
	case "up":
		if a.report.activeTab == 3 { // В табе История
			// Навигация по таблице
			a.reportScrollY--
			if a.reportScrollY < 0 {
				a.reportScrollY = 0
			}
		} else {
			if a.reportScrollY > 0 {
				a.reportScrollY--
			}
		}
	case "down":
		if a.report.activeTab == 3 { // В табе История
			// Навигация по таблице
			a.reportScrollY++
		} else {
			a.reportScrollY++
		}
	case "left", "a", "ф":
		// Переключение на предыдущую вкладку
		if a.report.activeTab > 0 {
			a.report.activeTab--
			a.reportScrollY = 0
		}
	case "right", "d", "в":
		// Переключение на следующую вкладку
		if a.report.activeTab < len(a.report.tabs)-1 {
			a.report.activeTab++
			a.reportScrollY = 0
		}
	case "1", "2", "3", "4", "5":
		// Быстрый переход к вкладке
		tabNum, _ := strconv.Atoi(msg.String())
		if tabNum > 0 && tabNum <= len(a.report.tabs) {
			a.report.activeTab = tabNum - 1
			a.reportScrollY = 0
		}
	case "f":
		// Переключение фильтра в истории
		if a.report.activeTab == 3 {
			switch a.report.filterState {
			case "all":
				a.report.filterState = "charging"
			case "charging":
				a.report.filterState = "discharging"
			case "discharging":
				a.report.filterState = "all"
			}
		}
	case "s":
		// Переключение сортировки в истории
		if a.report.activeTab == 3 {
			a.report.sortDesc = !a.report.sortDesc
		}
	case "r", "к":
		// Обновляем данные отчета
		a.reportScrollY = 0 // Сбрасываем скролл при обновлении
		a.report.lastUpdate = time.Now()
		return a, nil
	}
	
	// Обновляем счетчик анимации
	a.report.animationTick++
	
	return a, nil
}

// updateExport обрабатывает обновления экспорта
func (a *App) updateExport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.state = StateMenu
		a.exportStatus = "" // Очищаем статус при выходе
		return a, nil
	case "enter":
		// Генерируем имя файла с текущей датой в Documents
		documentsDir, err := getDocumentsDir()
		if err != nil {
			// Fallback на текущую директорию
			documentsDir = "."
		}
		filename := filepath.Join(documentsDir, fmt.Sprintf("batmon_report_%s.html", time.Now().Format("2006-01-02")))
		a.exportStatus = "Экспорт в процессе..."
		a.exportToHTMLAsync(filename)
		return a, nil
	}
	return a, nil
}

// exportToHTMLAsync выполняет экспорт в HTML асинхронно
func (a *App) exportToHTMLAsync(filename string) {
	go func() {
		// Создаем временное соединение с базой данных для экспорта
		db, err := initDB(getDBPath())
		if err != nil {
			a.exportStatus = "Ошибка подключения к БД"
			return
		}
		defer db.Close()
		
		// Генерируем данные для отчета
		data, err := generateReportData(db)
		if err != nil {
			a.exportStatus = "Ошибка генерации данных"
			return
		}
		
		// Экспортируем в HTML
		err = exportToHTML(data, filename)
		if err != nil {
			a.exportStatus = "Ошибка экспорта"
			return
		}
		
		a.exportStatus = fmt.Sprintf("Успешно экспортировано в %s", filename)
	}()
}

// generateUIReportData генерирует данные для UI отчета
func (a *App) generateUIReportData() (*ReportData, error) {
	// Создаем соединение с базой данных как в экспорте
	db, err := initDB(getDBPath())
	if err != nil {
		return nil, fmt.Errorf("ошибка подключения к БД: %w", err)
	}
	defer db.Close()
	
	data, err := generateReportData(db)
	if err != nil {
		return nil, fmt.Errorf("ошибка генерации данных: %w", err)
	}
	
	return &data, nil
}

// updateSettings обрабатывает обновления настроек
func (a *App) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й", "n", "N", "н", "Н":
		a.state = StateMenu
		return a, nil
	case "y", "Y", "д", "Д":
		err := a.clearDatabase()
		if err != nil {
			a.lastError = fmt.Errorf("ошибка очистки БД: %v", err)
		} else {
			a.lastError = nil
		}
		a.state = StateMenu
		return a, nil
	}
	return a, nil
}

// updateWelcome обрабатывает нажатия в экране приветствия
func (a *App) updateWelcome(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.dataService.Stop()
		return a, tea.Quit
	case "enter", " ":
		a.state = StateMenu
		return a, nil
	}
	return a, nil
}

// updateQuickDiag обрабатывает нажатия в режиме быстрой диагностики  
func (a *App) updateQuickDiag(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.state = StateMenu
		return a, nil
	}
	return a, nil
}

// updateHelp обрабатывает нажатия в режиме справки
func (a *App) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "й":
		a.state = StateMenu
		return a, nil
	}
	return a, nil
}

// updateComponentSizes обновляет размеры всех компонентов при изменении размера окна
func (a *App) updateComponentSizes() {
	// Обновляем размер списка меню
	a.menu.list.SetSize(a.windowWidth-2, a.windowHeight-4)
	
	// Обновляем размеры компонентов dashboard
	if a.state == StateDashboard {
		// Пересчитываем ширину прогресс-баров
		progressWidth := (a.windowWidth / 2) - 20
		if progressWidth < 20 {
			progressWidth = 20
		}
		if progressWidth > 40 {
			progressWidth = 40
		}
		
		// Обновляем ширину прогресс-баров
		a.dashboard.batteryGauge = progress.New(
			progress.WithDefaultGradient(),
			progress.WithWidth(progressWidth),
		)
		a.dashboard.wearGauge = progress.New(
			progress.WithDefaultGradient(),
			progress.WithWidth(progressWidth),
		)
		
		// Обновляем размеры таблицы измерений с фиксированными колонками
		columns := []table.Column{
			{Title: "Время", Width: 5},
			{Title: "Заряд", Width: 5},
			{Title: "Состояние", Width: 10},
			{Title: "Темп.", Width: 5},
		}
		
		a.dashboard.measureTable = table.New(
			table.WithColumns(columns),
			table.WithHeight(4), // Фиксированная высота для 4 записей
			table.WithFocused(false),
		)
		
		// Обновляем данные таблицы
		a.updateDashboardData()
	}
	
	// Обновляем размеры компонентов отчета
	if a.state == StateReport {
		a.report.viewHeight = a.windowHeight - 4
		
		// Обновляем размеры таблицы истории
		tableWidth := a.windowWidth - 10
		columnWidths := a.calculateReportTableColumnWidths(tableWidth)
		
		columns := []table.Column{
			{Title: "Время", Width: columnWidths[0]},
			{Title: "Заряд", Width: columnWidths[1]},
			{Title: "Состояние", Width: columnWidths[2]},
			{Title: "Циклы", Width: columnWidths[3]},
			{Title: "Темп.", Width: columnWidths[4]},
			{Title: "Износ", Width: columnWidths[5]},
		}
		
		tableHeight := min(20, a.windowHeight-10)
		a.report.historyTable = table.New(
			table.WithColumns(columns),
			table.WithHeight(tableHeight),
			table.WithFocused(false),
		)
	}
}

// calculateTableColumnWidths вычисляет ширину колонок для таблицы dashboard
func (a *App) calculateTableColumnWidths(totalWidth int) []int {
	// Фиксированные ширины для компактной таблицы
	// Время: 5 символов (HH:MM)
	// Заряд: 4 символа (100%)
	// Состояние: 10 символов
	// Темп: 4 символа (30°C)
	return []int{5, 4, 10, 4}
}

// calculateReportTableColumnWidths вычисляет ширину колонок для таблицы отчета
func (a *App) calculateReportTableColumnWidths(totalWidth int) []int {
	// Минимальные ширины колонок
	minWidths := []int{16, 6, 10, 6, 6, 6}
	
	// Если места недостаточно, используем минимальные ширины
	minTotal := 0
	for _, w := range minWidths {
		minTotal += w
	}
	
	if totalWidth <= minTotal+6 {
		return minWidths
	}
	
	// Распределяем дополнительное пространство
	extraSpace := totalWidth - minTotal - 6
	
	// Пропорции для дополнительного пространства
	widths := make([]int, 6)
	widths[0] = minWidths[0] + (extraSpace * 35 / 100) // Время
	widths[1] = minWidths[1] + (extraSpace * 10 / 100) // Заряд
	widths[2] = minWidths[2] + (extraSpace * 35 / 100) // Состояние
	widths[3] = minWidths[3] + (extraSpace * 5 / 100)  // Циклы
	widths[4] = minWidths[4] + (extraSpace * 10 / 100) // Темп
	widths[5] = minWidths[5] + (extraSpace * 5 / 100)  // Износ
	
	return widths
}

// View рендерит интерфейс
func (a *App) View() string {
	switch a.state {
	case StateWelcome:
		return a.renderWelcome()
	case StateMenu:
		return a.renderMenu()
	case StateDashboard:
		return a.renderDashboard()
	case StateReport:
		return a.renderReport()
	case StateQuickDiag:
		return a.renderQuickDiag()
	case StateExport:
		return a.renderExport()
	case StateSettings:
		return a.renderSettings()
	case StateHelp:
		return a.renderHelp()
	default:
		return "Неизвестное состояние приложения"
	}
}

// renderMenu рендерит главное меню
func (a *App) renderMenu() string {
	return lipgloss.NewStyle().
		Padding(1).
		Render(a.menu.list.View())
}

// renderDashboard рендерит dashboard
func (a *App) renderDashboard() string {
	if a.latest == nil {
		return a.renderLoadingScreen()
	}
	
	// Вычисляем размеры для адаптивной разметки
	contentWidth := a.windowWidth - 4   // Отступы
	contentHeight := a.windowHeight - 4 // Отступы
	
	if contentWidth < 60 || contentHeight < 20 {
		return a.renderCompactDashboard()
	}
	
	// Рендерим полный dashboard
	fullContent := a.renderFullDashboard(contentWidth, contentHeight)
	
	// Если контент не влезает по высоте, применяем скролл
	contentLines := strings.Split(fullContent, "\n")
	if len(contentLines) > contentHeight {
		// Применяем скролл
		start := a.dashboardScrollY
		end := start + contentHeight
		if end > len(contentLines) {
			end = len(contentLines)
		}
		if start > len(contentLines)-contentHeight {
			start = max(0, len(contentLines)-contentHeight)
			a.dashboardScrollY = start
		}
		
		scrolledContent := strings.Join(contentLines[start:end], "\n")
		
		// Добавляем индикатор скролла
		scrollInfo := ""
		if a.dashboardScrollY > 0 || end < len(contentLines) {
			scrollInfo = fmt.Sprintf("   ↕ Скролл: %d/%d (↑↓/kj)", a.dashboardScrollY+1, len(contentLines)-contentHeight+1)
			scrolledContent += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(scrollInfo)
		}
		
		return scrolledContent
	}
	
	return fullContent
}

// calculateMaxDashboardScroll вычисляет максимальное значение скролла для dashboard
func (a *App) calculateMaxDashboardScroll() int {
	if a.latest == nil {
		return 0
	}
	
	contentWidth := a.windowWidth - 4
	contentHeight := a.windowHeight - 4
	
	if contentWidth < 60 || contentHeight < 20 {
		return 0 // Компактный режим не скроллится
	}
	
	// Рендерим контент и считаем строки
	fullContent := a.renderFullDashboard(contentWidth, contentHeight)
	contentLines := strings.Split(fullContent, "\n")
	
	maxScroll := len(contentLines) - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	
	return maxScroll
}

// renderLoadingScreen показывает экран загрузки
func (a *App) renderLoadingScreen() string {
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Render("🔋 ПОЛНЫЙ АНАЛИЗ БАТАРЕИ") + "\n\n"
		
	loading := "🔄 Собираем данные о батарее...\n\n"
	
	instructions := lipgloss.NewStyle().
		Foreground(lipgloss.Color("10")).
		Bold(true).
		Render("📋 ЧТО НУЖНО ДЕЛАТЬ:") + "\n"
	instructions += "1. Оставьте программу работать\n"
	instructions += "2. Используйте MacBook как обычно\n"
	instructions += "3. Разрядите батарею до 10-0%\n"
	instructions += "4. После разрядки получите отчет\n\n"
	
	tips := lipgloss.NewStyle().
		Foreground(lipgloss.Color("11")).
		Bold(true).
		Render("💡 СОВЕТЫ:") + "\n"
	tips += "• Минимум 2-3 часа для качественного анализа\n"
	tips += "• Не закрывайте программу\n"
	tips += "• При низком заряде сохраните работу\n\n"
	
	// Статус caffeinate
	var caffeineStatus string
	if a.dataService != nil && a.dataService.caffeineActive {
		caffeineStatus = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			Render("☕ Предотвращение засыпания активно") + "\n\n"
	}
	
	controls := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Render("Нажмите 'q' для выхода в главное меню")
	
	content := title + loading + instructions + tips + caffeineStatus + controls
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(2).
		Width(60).
		Render(content)
}

// renderCompactDashboard рендерит компактную версию для маленьких экранов
func (a *App) renderCompactDashboard() string {
	// Простая визуализация для компактного режима
	batteryData := make([]float64, 0, len(a.measurements))
	for _, m := range a.measurements {
		batteryData = append(batteryData, float64(m.Percentage))
	}
	
	// Создаем простой спарклайн вручную
	sparklineStr := ""
	if len(batteryData) > 0 {
		for _, val := range batteryData[max(0, len(batteryData)-10):] {
			if val > 75 {
				sparklineStr += "█"
			} else if val > 50 {
				sparklineStr += "▓"
			} else if val > 25 {
				sparklineStr += "▒"
			} else {
				sparklineStr += "░"
			}
		}
	}
	
	content := fmt.Sprintf(`🔋 Мониторинг батареи

Заряд: %d%% │ %s
Состояние: %s
Циклы: %d │ Износ: %.1f%%
Температура: %d°C

⌨️  'q'/'й' - выход │ 'r'/'к' - обновить`,
		a.latest.Percentage,
		sparklineStr,
		a.latest.State,
		a.latest.CycleCount,
		computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap),
		a.latest.Temperature,
	)
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(getBatteryColor(a.latest.Percentage)).
		Padding(1).
		Render(content)
}

// renderFullDashboard рендерит полную версию dashboard
func (a *App) renderFullDashboard(width, height int) string {
	// Данные для графиков
	batteryData := make([]float64, 0, len(a.measurements))
	capacityData := make([]float64, 0, len(a.measurements))
	
	for _, m := range a.measurements {
		batteryData = append(batteryData, float64(m.Percentage))
		capacityData = append(capacityData, float64(m.CurrentCapacity))
	}
	
	// Адаптивные размеры для графиков
	// Учитываем отступы и границы
	chartWidth := (width - 4) / 2  // Делим пополам с учетом отступов
	chartHeight := (height - 6) / 2 // Делим пополам с учетом заголовков
	
	// Минимальные размеры для читабельности
	if chartWidth < 30 {
		chartWidth = 30
	}
	if chartHeight < 10 {
		chartHeight = 10
	}
	
	// Максимальные размеры для больших экранов
	if chartWidth > 80 {
		chartWidth = 80
	}
	if chartHeight > 30 {
		chartHeight = 30
	}
	
	var batteryChartContent, capacityChartContent string
	
	if len(batteryData) > 0 {
		batteryChart := NewBatteryChart(chartWidth, chartHeight)
		batteryChart.SetData(batteryData)
		batteryChartContent = batteryChart.Render()
	} else {
		emptyStyle := lipgloss.NewStyle().
			Width(chartWidth).
			Height(chartHeight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Align(lipgloss.Center, lipgloss.Center)
		batteryChartContent = emptyStyle.Render("📊 График заряда\n\nНет данных для отображения")
	}
	
	if len(capacityData) > 0 {
		capacityChart := NewCapacityChart(chartWidth, chartHeight)  
		capacityChart.SetData(capacityData)
		capacityChartContent = capacityChart.Render()
	} else {
		emptyStyle := lipgloss.NewStyle().
			Width(chartWidth).
			Height(chartHeight).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Align(lipgloss.Center, lipgloss.Center)
		capacityChartContent = emptyStyle.Render("📈 График емкости\n\nНет данных для отображения")
	}
	
	// Информационная панель с адаптивными размерами
	infoPanelWidth := (width - 4) / 2
	infoPanelHeight := (height - 6) / 2
	infoPanel := a.renderInfoPanel(infoPanelWidth, infoPanelHeight)
	
	// Статистика с адаптивными размерами
	statsPanelWidth := (width - 4) / 2
	statsPanelHeight := (height - 6) / 2
	statsPanel := a.renderStatsPanel(statsPanelWidth, statsPanelHeight)
	
	// Возвращаем оригинальную компоновку: графики сверху, текстовые блоки снизу
	topRow := lipgloss.JoinHorizontal(lipgloss.Top,
		batteryChartContent,
		" ",
		capacityChartContent,
	)
	
	bottomRow := lipgloss.JoinHorizontal(lipgloss.Top,
		infoPanel,
		" ",
		statsPanel,
	)
	
	// Вертикальная компоновка с разделителем
	return lipgloss.JoinVertical(lipgloss.Left,
		topRow,
		"",
		bottomRow,
	)
}

// renderInfoPanel рендерит информационную панель
func (a *App) renderInfoPanel(width, height int) string {
	wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
	
	// Вычисляем проценты для прогресс-баров
	batteryPercent := float64(a.latest.Percentage) / 100.0
	wearPercent := wear / 100.0
	
	// Рендерим прогресс-бары
	batteryBar := a.dashboard.batteryGauge.ViewAs(batteryPercent)
	wearBar := a.dashboard.wearGauge.ViewAs(wearPercent)
	
	// Вычисляем качество данных для анализа
	dataPoints := len(a.measurements)
	var dataHours float64
	if dataPoints > 1 {
		// Используем реальное время между первым и последним измерением
		firstTime, _ := time.Parse(time.RFC3339, a.measurements[0].Timestamp)
		lastTime, _ := time.Parse(time.RFC3339, a.measurements[dataPoints-1].Timestamp)
		dataHours = lastTime.Sub(firstTime).Hours()
	} else if dataPoints == 1 {
		// Если только одно измерение, считаем как 30 секунд (интервал сбора)
		dataHours = 0.5 / 60.0 // 30 секунд в часах
	} else {
		dataHours = 0
	}
	dataQuality := "Недостаточно"
	dataColor := "9" // красный
	if dataHours >= 2.0 {
		dataQuality = "Отлично"
		dataColor = "10" // зеленый
	} else if dataHours >= 1.0 {
		dataQuality = "Хорошо"
		dataColor = "11" // желтый
	}
	
	content := fmt.Sprintf(`🔋 Текущее состояние

⚡ Заряд: %d%%
%s

📉 Износ: %.1f%%
%s

🔄 Состояние: %s
🔁 Циклы: %d
🌡️  Температура: %d°C
⚡ Напряжение: %d мВ
🔌 Ток: %d мА

💚 Здоровье: %s

📊 Качество данных: %s
⏱️  Собрано: %.1fч (%d точек)`,
		a.latest.Percentage,
		batteryBar,
		wear,
		wearBar,
		formatBatteryState(a.latest.State),
		a.latest.CycleCount,
		a.latest.Temperature,
		a.latest.Voltage,
		a.latest.Amperage,
		getBatteryHealthStatus(wear, a.latest.CycleCount),
		lipgloss.NewStyle().Foreground(lipgloss.Color(dataColor)).Render(dataQuality),
		dataHours,
		dataPoints,
	)
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(getBatteryColor(a.latest.Percentage)).
		Padding(1).
		Width(width-2).
		Height(height).
		Render(content)
}

// renderStatsPanel рендерит панель со статистикой и управлением
func (a *App) renderStatsPanel(width, height int) string {
	// Обновляем данные таблицы
	a.updateMeasureTable()
	
	// Рендерим таблицу
	tableView := a.dashboard.measureTable.View()
	
	// Создаем контент с правильным форматированием
	var contentBuilder strings.Builder
	contentBuilder.WriteString("Последние измерения\n")
	contentBuilder.WriteString(tableView)
	contentBuilder.WriteString("\n\n")
	contentBuilder.WriteString("Управление:\n")
	contentBuilder.WriteString("  'q'/'й' - выход\n")
	contentBuilder.WriteString("  'r'/'к' - обновить\n")
	contentBuilder.WriteString("  ↑↓/jk - скролл")
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1).
		Width(width-2).
		Height(height).
		Render(contentBuilder.String())
}

// updateMeasureTable обновляет данные в таблице измерений
func (a *App) updateMeasureTable() {
	rows := make([]table.Row, 0)
	
	// Берем последние 4 измерения
	recentCount := 4
	if len(a.measurements) < recentCount {
		recentCount = len(a.measurements)
	}
	
	if recentCount > 0 {
		start := len(a.measurements) - recentCount
		for i := start; i < len(a.measurements); i++ {
			m := a.measurements[i]
			
			// Форматируем время
			timeStr := "?"
			if len(m.Timestamp) >= 19 {
				timeStr = m.Timestamp[11:16] // HH:MM
			}
			
			// Форматируем состояние
			stateStr := m.State
			if len(stateStr) > 10 {
				stateStr = stateStr[:9] + "."
			}
			
			// Форматируем температуру
			tempStr := "-"
			if m.Temperature > 0 {
				tempStr = fmt.Sprintf("%d°", m.Temperature)
			}
			
			// Форматируем заряд компактно
			chargeStr := fmt.Sprintf("%d%%", m.Percentage)
			if m.Percentage == 100 {
				chargeStr = "100"
			}
			
			row := table.Row{
				timeStr,
				chargeStr,
				stateStr,
				tempStr,
			}
			
			rows = append(rows, row)
		}
	}
	
	a.dashboard.measureTable.SetRows(rows)
}

// Вспомогательные функции для стилизации
func getBatteryColor(percentage int) lipgloss.Color {
	switch {
	case percentage >= 50:
		return lipgloss.Color("46") // Зеленый
	case percentage >= 20:
		return lipgloss.Color("226") // Желтый
	default:
		return lipgloss.Color("196") // Красный
	}
}

func getTemperatureColor(temp int) lipgloss.Color {
	switch {
	case temp <= 30:
		return lipgloss.Color("46") // Зеленый
	case temp <= 40:
		return lipgloss.Color("226") // Желтый  
	default:
		return lipgloss.Color("196") // Красный
	}
}

func getWearColor(wear float64) lipgloss.Color {
	switch {
	case wear < 10:
		return lipgloss.Color("46") // Зеленый
	case wear < 20:
		return lipgloss.Color("226") // Желтый
	default:
		return lipgloss.Color("196") // Красный
	}
}

func getCycleColor(cycles int) lipgloss.Color {
	switch {
	case cycles < 300:
		return lipgloss.Color("46") // Зеленый
	case cycles < 1000:
		return lipgloss.Color("226") // Желтый
	default:
		return lipgloss.Color("196") // Красный
	}
}

func getBatteryHealthColor(wear float64, cycles int) string {
	if wear < 20 && cycles < 1000 {
		return "10" // Зеленый
	} else if wear < 30 && cycles < 1500 {
		return "11" // Желтый
	} else {
		return "9" // Красный
	}
}

func formatBatteryState(state string) string {
	switch state {
	case "charging":
		return "🔌 Зарядка"
	case "discharging":
		return "🔋 Разрядка"
	case "charged":
		return "✅ Заряжена"
	default:
		return state
	}
}

func getBatteryHealthStatus(wear float64, cycles int) string {
	switch {
	case wear < 5 && cycles < 300:
		return "Отличное"
	case wear < 10 && cycles < 500:
		return "Хорошее"  
	case wear < 20 && cycles < 800:
		return "Удовлетворительное"
	default:
		return "Требует внимания"
	}
}

// renderReport рендерит детальный отчет с полной аналитикой
func (a *App) renderReport() string {
	// Получаем полные данные аналитики
	reportData, err := a.generateUIReportData()
	if err != nil {
		return fmt.Sprintf("❌ Ошибка загрузки отчета: %v\nНажмите 'q' для выхода в меню", err)
	}

	// Создаем контент в зависимости от активной вкладки
	var tabContent string
	switch a.report.activeTab {
	case 0: // Обзор
		tabContent = a.renderReportOverview(reportData)
	case 1: // Графики
		tabContent = a.renderReportCharts(reportData)
	case 2: // Аномалии
		tabContent = a.renderReportAnomalies(reportData)
	case 3: // История
		tabContent = a.renderReportHistory(reportData)
	case 4: // Прогнозы
		tabContent = a.renderReportPredictions(reportData)
	default:
		tabContent = a.renderReportOverview(reportData)
	}
	
	// Рендерим табы
	tabBar := a.renderTabBar()
	
	// Добавляем панель управления
	helpBar := a.renderReportHelpBar()
	
	// Вычисляем доступное пространство для контента
	contentHeight := a.windowHeight - 8 // Учитываем табы, помощь, отступы
	
	// Применяем скролл если нужно
	scrolledContent := a.applyReportScroll(tabContent, contentHeight)
	
	// Создаем финальный контент
	var content strings.Builder
	content.WriteString(tabBar)
	content.WriteString("\n")
	content.WriteString(scrolledContent)
	content.WriteString("\n")
	content.WriteString(helpBar)
	
	// Оборачиваем в компактную рамку
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(a.getTabColor()).
		Padding(1).
		Width(a.windowWidth-4).
		Render(content.String())
}

// applyReportScroll применяет скролл к контенту вкладки
func (a *App) applyReportScroll(content string, maxHeight int) string {
	contentLines := strings.Split(content, "\n")
	
	if len(contentLines) <= maxHeight {
		// Контент влезает полностью
		return content
	}
	
	// Применяем скролл
	start := a.reportScrollY
	end := start + maxHeight
	
	// Корректируем границы
	if end > len(contentLines) {
		end = len(contentLines)
	}
	if start > len(contentLines)-maxHeight {
		start = max(0, len(contentLines)-maxHeight)
		a.reportScrollY = start
	}
	
	scrolledLines := contentLines[start:end]
	scrolledContent := strings.Join(scrolledLines, "\n")
	
	// Добавляем индикатор скролла
	if start > 0 || end < len(contentLines) {
		scrollInfo := fmt.Sprintf("   ↕ %d/%d", start+1, len(contentLines)-maxHeight+1)
		scrolledContent += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(scrollInfo)
	}
	
	return scrolledContent
}

// buildReportContent создает содержимое отчета на основе данных аналитики
func (a *App) buildReportContent(data *ReportData) string {
	var content strings.Builder
	
	// Заголовок
	content.WriteString("📊 Детальный отчет о состоянии батареи\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	// 1. Заголовочная панель с ключевыми метриками
	content.WriteString("🔋 ОБЩЕЕ СОСТОЯНИЕ\n")
	content.WriteString("┌─────────────────────────────────────────────────┐\n")
	
	healthStatus := getBatteryHealthStatus(data.Wear, data.Latest.CycleCount)
	healthEmoji := getHealthEmoji(data.Wear)
	content.WriteString(fmt.Sprintf("│ Состояние: %s %s\n", healthEmoji, healthStatus))
	
	// Рейтинг здоровья с прогресс-баром
	if healthAnalysis, ok := data.HealthAnalysis["health_score"].(float64); ok {
		healthScore := int(healthAnalysis)
		progressBar := createProgressBar(healthScore, 100, 20)
		content.WriteString(fmt.Sprintf("│ Рейтинг:   %s %d/100\n", progressBar, healthScore))
	}
	
	content.WriteString(fmt.Sprintf("│ Износ:     %.1f%%\n", data.Wear))
	content.WriteString(fmt.Sprintf("│ Циклы:     %d\n", data.Latest.CycleCount))
	content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	
	// 2. Текущее состояние
	content.WriteString("⚡ ТЕКУЩЕЕ СОСТОЯНИЕ\n")
	content.WriteString("┌─────────────────────────────────────────────────┐\n")
	
	// Заряд с прогресс-баром
	chargeBar := createProgressBar(data.Latest.Percentage, 100, 25)
	content.WriteString(fmt.Sprintf("│ Заряд:     %s %d%%\n", chargeBar, data.Latest.Percentage))
	
	stateEmoji := getStateEmoji(data.Latest.State)
	content.WriteString(fmt.Sprintf("│ Статус:    %s %s\n", stateEmoji, formatBatteryState(data.Latest.State)))
	
	// Прогнозируемое время
	if data.RemainingTime > 0 {
		content.WriteString(fmt.Sprintf("│ Осталось:  %s\n", formatDuration(data.RemainingTime)))
	}
	
	tempEmoji := getTempEmoji(data.Latest.Temperature)
	content.WriteString(fmt.Sprintf("│ Темп-ра:   %s %d°C\n", tempEmoji, data.Latest.Temperature))
	content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	
	// 3. Анализ производительности
	content.WriteString("📈 АНАЛИЗ ПРОИЗВОДИТЕЛЬНОСТИ\n")
	content.WriteString("┌─────────────────────────────────────────────────┐\n")
	content.WriteString(fmt.Sprintf("│ Скорость разряда:   %.1f мА/ч\n", data.RobustRate))
	if data.Latest.Power != 0 {
		content.WriteString(fmt.Sprintf("│ Потребление:        %d мВт\n", abs(data.Latest.Power)))
	}
	if data.Latest.Voltage != 0 {
		content.WriteString(fmt.Sprintf("│ Напряжение:         %.2f В\n", float64(data.Latest.Voltage)/1000))
	}
	content.WriteString(fmt.Sprintf("│ Валидных интервалов: %d\n", data.ValidIntervals))
	content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	
	// 4. Здоровье батареи
	content.WriteString("💊 ЗДОРОВЬЕ БАТАРЕИ\n")
	content.WriteString("┌─────────────────────────────────────────────────┐\n")
	content.WriteString(fmt.Sprintf("│ Текущая емкость:    %d мАч\n", data.Latest.CurrentCapacity))
	content.WriteString(fmt.Sprintf("│ Полная емкость:     %d мАч\n", data.Latest.FullChargeCap))
	content.WriteString(fmt.Sprintf("│ Проектная емкость:  %d мАч\n", data.Latest.DesignCapacity))
	
	if data.Latest.AppleCondition != "" {
		content.WriteString(fmt.Sprintf("│ Статус Apple:       %s\n", data.Latest.AppleCondition))
	}
	
	content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	
	// 5. Обнаруженные проблемы и рекомендации
	if len(data.Anomalies) > 0 {
		content.WriteString("⚠️  ОБНАРУЖЕННЫЕ ПРОБЛЕМЫ\n")
		content.WriteString("┌─────────────────────────────────────────────────┐\n")
		for _, anomaly := range data.Anomalies {
			content.WriteString(fmt.Sprintf("│ • %s\n", anomaly))
		}
		content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	}
	
	if len(data.Recommendations) > 0 {
		content.WriteString("💡 РЕКОМЕНДАЦИИ\n")
		content.WriteString("┌─────────────────────────────────────────────────┐\n")
		for _, rec := range data.Recommendations {
			content.WriteString(fmt.Sprintf("│ • %s\n", rec))
		}
		content.WriteString("└─────────────────────────────────────────────────┘\n\n")
	}
	
	// 6. История измерений (компактная)
	content.WriteString("📋 ПОСЛЕДНИЕ ИЗМЕРЕНИЯ\n")
	content.WriteString("┌──────────┬─────────┬─────────────────┬──────────┐\n")
	content.WriteString("│   Время  │ Заряд % │    Состояние    │ Темп °C  │\n")
	content.WriteString("├──────────┼─────────┼─────────────────┼──────────┤\n")
	
	recentCount := 10
	if len(data.Measurements) < recentCount {
		recentCount = len(data.Measurements)
	}
	
	for i := len(data.Measurements) - recentCount; i < len(data.Measurements); i++ {
		m := data.Measurements[i]
		timeStr := m.Timestamp[11:19] // HH:MM:SS
		stateStr := formatBatteryStateShort(m.State)
		content.WriteString(fmt.Sprintf("│ %8s │   %3d   │ %-15s │    %2d    │\n", 
			timeStr, m.Percentage, stateStr, m.Temperature))
	}
	content.WriteString("└──────────┴─────────┴─────────────────┴──────────┘\n")
	
	return content.String()
}

// Вспомогательные функции для отображения отчета

// getHealthEmoji возвращает эмодзи для состояния здоровья батареи
func getHealthEmoji(wear float64) string {
	switch {
	case wear < 5:
		return "💚"
	case wear < 10:
		return "🟢"
	case wear < 20:
		return "🟡"
	case wear < 30:
		return "🟠"
	default:
		return "🔴"
	}
}

// getStateEmoji возвращает эмодзи для состояния батареи
func getStateEmoji(state string) string {
	switch state {
	case "charging":
		return "🔌"
	case "discharging":
		return "🔋"
	case "charged":
		return "✅"
	case "AC":
		return "⚡"
	default:
		return "❓"
	}
}

// getTempEmoji возвращает эмодзи для температуры
func getTempEmoji(temp int) string {
	switch {
	case temp < 15:
		return "🧊"
	case temp < 25:
		return "❄️"
	case temp < 35:
		return "🌡️"
	case temp < 45:
		return "🔥"
	default:
		return "🌋"
	}
}

// createProgressBar создает ASCII прогресс-бар
func createProgressBar(current, max, width int) string {
	if max == 0 {
		return strings.Repeat("░", width)
	}
	
	filled := (current * width) / max
	if filled > width {
		filled = width
	}
	
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s]", bar)
}

// formatBatteryStateShort возвращает короткое описание состояния батареи
func formatBatteryStateShort(state string) string {
	switch state {
	case "charging":
		return "Зарядка"
	case "discharging":
		return "Разрядка"
	case "charged":
		return "Заряжена"
	case "AC":
		return "От сети"
	default:
		return state
	}
}

// formatDuration форматирует время в читаемый вид
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	
	if hours > 0 {
		return fmt.Sprintf("%d ч %d мин", hours, minutes)
	}
	return fmt.Sprintf("%d мин", minutes)
}

// renderTabBar рендерит компактную панель вкладок
func (a *App) renderTabBar() string {
	var tabs []string
	
	// Компактные названия вкладок
	compactTabs := []string{"Обзор", "Графики", "Аномалии", "История", "Прогноз"}
	
	for i, tab := range compactTabs {
		if i >= len(a.report.tabs) {
			break
		}
		
		style := lipgloss.NewStyle().
			Padding(0, 1)
		
		if i == a.report.activeTab {
			// Активная вкладка
			style = style.
				Background(a.getTabColor()).
				Foreground(lipgloss.Color("230")).
				Bold(true)
		} else {
			// Неактивная вкладка
			style = style.
				Foreground(lipgloss.Color("241"))
		}
		
		// Компактный формат
		tabText := fmt.Sprintf("%d.%s", i+1, tab)
		tabs = append(tabs, style.Render(tabText))
	}
	
	// Разделители между вкладками
	separator := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("│")
	return strings.Join(tabs, separator)
}

// getTabColor возвращает цвет для активной вкладки
func (a *App) getTabColor() lipgloss.Color {
	colors := []lipgloss.Color{
		lipgloss.Color("62"),  // Обзор - синий
		lipgloss.Color("214"), // Графики - оранжевый
		lipgloss.Color("196"), // Аномалии - красный
		lipgloss.Color("82"),  // История - зеленый
		lipgloss.Color("99"),  // Прогнозы - фиолетовый
	}
	
	if a.report.activeTab < len(colors) {
		return colors[a.report.activeTab]
	}
	return lipgloss.Color("240")
}

// renderReportHelpBar рендерит компактную панель помощи
func (a *App) renderReportHelpBar() string {
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Padding(0, 1)
	
	// Базовые команды
	help := []string{
		"←→",  // Переключение вкладок
		"1-5", // Быстрый переход
		"↑↓",  // Скролл
		"r",   // Обновить
		"q",   // Выход
	}
	
	// Специфичные для вкладки команды
	if a.report.activeTab == 3 { // История
		help = append([]string{"f", "s"}, help...)
	}
	
	// Компактное отображение с минимальными разделителями
	separator := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("·")
	return helpStyle.Render(strings.Join(help, separator))
}

// renderReportOverview рендерит вкладку обзора с виджетами
func (a *App) renderReportOverview(data *ReportData) string {
	// Создаем виджеты для обзора
	widgets := a.createOverviewWidgets(data)
	
	// Определяем раскладку в зависимости от размера экрана
	if a.windowWidth < 100 {
		// Вертикальная раскладка для узких экранов
		return a.renderWidgetsVertical(widgets)
	}
	
	// Сетка 2x2 или 3x2 для широких экранов
	return a.renderWidgetsGrid(widgets)
}

// createOverviewWidgets создает виджеты для обзора
func (a *App) createOverviewWidgets(data *ReportData) []ReportWidget {
	widgets := []ReportWidget{}
	
	// Виджет здоровья батареи
	healthScore := 70.0
	if score, ok := data.HealthAnalysis["health_score"].(float64); ok {
		healthScore = score
	}
	
	widgets = append(widgets, ReportWidget{
		title:      "💚 Здоровье батареи",
		widgetType: "gauge",
		value:      healthScore,
		maxValue:   100,
		color:      a.getHealthColor(healthScore),
		icon:       a.getHealthIcon(healthScore),
	})
	
	// Виджет текущего заряда
	widgets = append(widgets, ReportWidget{
		title:      "🔋 Текущий заряд",
		widgetType: "gauge",
		value:      float64(data.Latest.Percentage),
		maxValue:   100,
		color:      getBatteryColor(data.Latest.Percentage),
		icon:       "⚡",
	})
	
	// Виджет износа
	widgets = append(widgets, ReportWidget{
		title:      "⚙️ Износ батареи",
		widgetType: "gauge",
		value:      data.Wear,
		maxValue:   30, // Максимально допустимый износ
		color:      a.getWearColor(data.Wear),
		icon:       "📉",
	})
	
	// Виджет циклов
	cyclePercent := float64(data.Latest.CycleCount) / 1000.0 * 100
	widgets = append(widgets, ReportWidget{
		title:      "🔄 Циклы зарядки",
		widgetType: "info",
		content:    fmt.Sprintf("%d / 1000", data.Latest.CycleCount),
		value:      cyclePercent,
		maxValue:   100,
		color:      a.getCycleColor(data.Latest.CycleCount),
		icon:       "♻️",
	})
	
	// Виджет времени работы
	if data.RemainingTime > 0 {
		widgets = append(widgets, ReportWidget{
			title:      "⏱️ Осталось времени",
			widgetType: "info",
			content:    formatDuration(data.RemainingTime),
			color:      lipgloss.Color("82"),
			icon:       "⏰",
		})
	}
	
	// Виджет температуры
	widgets = append(widgets, ReportWidget{
		title:      "🌡️ Температура",
		widgetType: "info",
		content:    fmt.Sprintf("%d°C", data.Latest.Temperature),
		color:      a.getTempColor(data.Latest.Temperature),
		icon:       getTempEmoji(data.Latest.Temperature),
	})
	
	return widgets
}

// renderWidgetsGrid рендерит виджеты в компактной сетке
func (a *App) renderWidgetsGrid(widgets []ReportWidget) string {
	var rows []string
	
	// Более умный адаптивный расчет
	availableWidth := a.windowWidth - 8  // Учитываем отступы интерфейса
	availableHeight := a.windowHeight - 8
	numColumns := 2
	
	// Адаптируем количество колонок под размер экрана
	if availableWidth < 50 {
		numColumns = 1
	} else if availableWidth > 120 {
		numColumns = 3
	} else if availableWidth > 200 {
		numColumns = 4
	}
	
	// Супер компактные размеры виджетов
	widgetWidth := max(25, (availableWidth - (numColumns-1)*2) / numColumns)
	widgetHeight := max(4, min(6, availableHeight / ((len(widgets)+numColumns-1)/numColumns)))  // Макс. 6 строк на виджет
	
	for i := 0; i < len(widgets); i += numColumns {
		var row []string
		
		for j := 0; j < numColumns && i+j < len(widgets); j++ {
			widget := a.renderCompactWidget(widgets[i+j], widgetWidth, widgetHeight)
			row = append(row, widget)
		}
		
		// Заполняем пустые места если нужно
		for len(row) < numColumns && numColumns > 1 {
			emptySpace := lipgloss.NewStyle().Width(widgetWidth).Height(widgetHeight).Render("")
			row = append(row, emptySpace)
		}
		
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	}
	
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderWidgetsVertical рендерит виджеты вертикально
func (a *App) renderWidgetsVertical(widgets []ReportWidget) string {
	var rows []string
	widgetWidth := max(30, a.windowWidth - 8)
	widgetHeight := max(4, min(6, (a.windowHeight-8) / len(widgets)))  // Компактнее
	
	for _, widget := range widgets {
		rows = append(rows, a.renderCompactWidget(widget, widgetWidth, widgetHeight))
	}
	
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderCompactWidget рендерит супер компактный виджет
func (a *App) renderCompactWidget(widget ReportWidget, width, height int) string {
	// Минимальные размеры для максимальной компактности
	adaptiveWidth := max(25, min(width, 45))
	adaptiveHeight := max(4, min(height, 6))  // Уменьшили минимальную высоту
	
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(widget.color).
		Width(adaptiveWidth).
		Height(adaptiveHeight).
		Padding(0, 1).  // Убрали вертикальные отступы
		Margin(0, 1, 0, 0)  // Убрали нижний отступ
	
	var content strings.Builder
	
	// Компактный заголовок
	titleStyle := lipgloss.NewStyle().
		Foreground(widget.color).
		Bold(true)
	
	// Убираем эмодзи из заголовка для экономии места
	cleanTitle := strings.ReplaceAll(widget.title, "💚 ", "")
	cleanTitle = strings.ReplaceAll(cleanTitle, "🔋 ", "")
	cleanTitle = strings.ReplaceAll(cleanTitle, "⚙️ ", "")
	cleanTitle = strings.ReplaceAll(cleanTitle, "🔥 ", "")
	cleanTitle = strings.ReplaceAll(cleanTitle, "📊 ", "")
	cleanTitle = strings.ReplaceAll(cleanTitle, "⏱️ ", "")
	
	if len(cleanTitle) > adaptiveWidth-4 {
		cleanTitle = cleanTitle[:adaptiveWidth-7] + "..."
	}
	
	content.WriteString(titleStyle.Render(cleanTitle))
	content.WriteString("\n")
	
	switch widget.widgetType {
	case "gauge":
		// Супер компактный прогресс-бар в одной строке с процентами
		barWidth := max(8, adaptiveWidth-10)
		bar := a.renderCompactProgressBar(widget.value, widget.maxValue, barWidth)
		
		// Процент справа от бара
		percentage := (widget.value / widget.maxValue) * 100
		valueStr := fmt.Sprintf("%.0f%%", percentage)
		
		// Все в одной строке
		progressLine := bar + " " + lipgloss.NewStyle().Foreground(widget.color).Bold(true).Render(valueStr)
		content.WriteString(progressLine)
		
	case "info":
		// Супер компактная информация - только первая строка
		infoLines := strings.Split(widget.content, "\n")
		if len(infoLines) > 0 {
			line := infoLines[0]
			if len(line) > adaptiveWidth-4 {
				line = line[:adaptiveWidth-7] + "..."
			}
			content.WriteString(line)
		}
		
	case "alert":
		// Компактное предупреждение
		alertStyle := lipgloss.NewStyle().
			Foreground(widget.color).
			Background(lipgloss.Color("52")).
			Padding(0, 1)
		
		alertText := widget.content
		if len(alertText) > adaptiveWidth-6 {
			alertText = alertText[:adaptiveWidth-9] + "..."
		}
		content.WriteString(alertStyle.Render(alertText))
		
	default:
		// Обычное содержимое
		if len(widget.content) > adaptiveWidth-4 {
			content.WriteString(widget.content[:adaptiveWidth-7] + "...")
		} else {
			content.WriteString(widget.content)
		}
	}
	
	return style.Render(content.String())
}

// renderCompactProgressBar рендерит компактный прогресс-бар
func (a *App) renderCompactProgressBar(value, maxValue float64, width int) string {
	if maxValue == 0 {
		return strings.Repeat("░", width)
	}
	
	percentage := value / maxValue
	if percentage > 1 {
		percentage = 1
	}
	
	filled := int(percentage * float64(width))
	
	// Используем простые символы для лучшей совместимости
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	
	// Цветовая градация
	barStyle := lipgloss.NewStyle()
	if percentage > 0.7 {
		barStyle = barStyle.Foreground(lipgloss.Color("46")) // Зеленый
	} else if percentage > 0.4 {
		barStyle = barStyle.Foreground(lipgloss.Color("226")) // Желтый
	} else {
		barStyle = barStyle.Foreground(lipgloss.Color("196")) // Красный
	}
	
	return barStyle.Render(bar)
}

// renderWidget рендерит отдельный виджет
func (a *App) renderWidget(widget ReportWidget, width int) string {
	// Адаптивная ширина с ограничениями
	adaptiveWidth := width
	if adaptiveWidth < 20 {
		adaptiveWidth = 20
	}
	if adaptiveWidth > 100 {
		adaptiveWidth = 100
	}
	
	// Адаптивные отступы в зависимости от ширины
	padding := 1
	margin := 1
	if adaptiveWidth < 30 {
		padding = 0
		margin = 0
	}
	
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(widget.color).
		Width(adaptiveWidth).
		Padding(padding).
		Margin(0, margin, 1, 0)
	
	var content strings.Builder
	
	// Заголовок с иконкой
	titleStyle := lipgloss.NewStyle().
		Foreground(widget.color).
		Bold(true).
		MaxWidth(adaptiveWidth - 4) // Учитываем границы и отступы
	content.WriteString(titleStyle.Render(widget.title))
	content.WriteString("\n")
	
	// Внутренняя ширина для контента
	contentWidth := adaptiveWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	
	switch widget.widgetType {
	case "gauge":
		// Адаптивный прогресс-бар
		barWidth := contentWidth - 2
		if barWidth < 10 {
			barWidth = 10
		}
		bar := a.renderAnimatedProgressBar(widget.value, widget.maxValue, barWidth)
		content.WriteString(bar)
		content.WriteString("\n")
		
		// Форматируем значения в зависимости от доступного места
		if contentWidth > 20 {
			content.WriteString(fmt.Sprintf("%.1f / %.0f", widget.value, widget.maxValue))
		} else {
			content.WriteString(fmt.Sprintf("%.0f%%", (widget.value/widget.maxValue)*100))
		}
		
	case "chart":
		// Адаптивный мини-график
		if contentWidth > 15 {
			content.WriteString(widget.content)
		} else {
			// Компактное представление для узких виджетов
			content.WriteString("📊")
		}
		
	case "info":
		// Информационный виджет с переносом текста
		infoStyle := lipgloss.NewStyle().
			Foreground(widget.color).
			Align(lipgloss.Center).
			MaxWidth(contentWidth)
		content.WriteString(infoStyle.Render(widget.content))
		
	case "alert":
		// Предупреждение с адаптивным размером
		alertStyle := lipgloss.NewStyle().
			Foreground(widget.color).
			Background(lipgloss.Color("52")).
			Padding(0, min(1, contentWidth/20)). // Адаптивные отступы
			MaxWidth(contentWidth)
		content.WriteString(alertStyle.Render(widget.content))
	}
	
	return style.Render(content.String())
}

// renderAnimatedProgressBar рендерит анимированный прогресс-бар
func (a *App) renderAnimatedProgressBar(value, maxValue float64, width int) string {
	if maxValue == 0 {
		return strings.Repeat("░", width)
	}
	
	percentage := value / maxValue
	if percentage > 1 {
		percentage = 1
	}
	
	filled := int(percentage * float64(width))
	
	// Добавляем анимацию для заполнения
	animChar := "█"
	if a.report.animationTick%4 < 2 && filled < width {
		animChar = "▓"
	}
	
	bar := strings.Repeat("█", filled)
	if filled < width {
		bar += animChar
		bar += strings.Repeat("░", width-filled-1)
	}
	
	// Добавляем цветовую градацию
	barStyle := lipgloss.NewStyle()
	if percentage > 0.7 {
		barStyle = barStyle.Foreground(lipgloss.Color("82"))
	} else if percentage > 0.4 {
		barStyle = barStyle.Foreground(lipgloss.Color("226"))
	} else {
		barStyle = barStyle.Foreground(lipgloss.Color("196"))
	}
	
	return fmt.Sprintf("[%s]", barStyle.Render(bar))
}

// Вспомогательные функции для определения цветов
func (a *App) getHealthColor(score float64) lipgloss.Color {
	if score >= 80 {
		return lipgloss.Color("82")
	} else if score >= 60 {
		return lipgloss.Color("226")
	} else if score >= 40 {
		return lipgloss.Color("214")
	}
	return lipgloss.Color("196")
}

func (a *App) getHealthIcon(score float64) string {
	if score >= 80 {
		return "💚"
	} else if score >= 60 {
		return "💛"
	} else if score >= 40 {
		return "🧡"
	}
	return "❤️"
}

func (a *App) getWearColor(wear float64) lipgloss.Color {
	if wear < 10 {
		return lipgloss.Color("82")
	} else if wear < 20 {
		return lipgloss.Color("226")
	}
	return lipgloss.Color("196")
}

func (a *App) getCycleColor(cycles int) lipgloss.Color {
	if cycles < 300 {
		return lipgloss.Color("82")
	} else if cycles < 600 {
		return lipgloss.Color("226")
	} else if cycles < 900 {
		return lipgloss.Color("214")
	}
	return lipgloss.Color("196")
}

func (a *App) getTempColor(temp int) lipgloss.Color {
	if temp < 30 {
		return lipgloss.Color("82")
	} else if temp < 40 {
		return lipgloss.Color("226")
	} else if temp < 50 {
		return lipgloss.Color("214")
	}
	return lipgloss.Color("196")
}

// renderReportCharts рендерит вкладку с графиками
func (a *App) renderReportCharts(data *ReportData) string {
	var content strings.Builder
	
	content.WriteString("📈 Графики производительности батареи\n")
	content.WriteString(strings.Repeat("─", 50) + "\n\n")
	
	// График заряда за последние измерения
	content.WriteString("🔋 История заряда (последние 24 часа)\n")
	content.WriteString(a.renderChargeChart(data.Measurements))
	content.WriteString("\n\n")
	
	// График скорости разряда
	content.WriteString("⚡ Скорость разряда\n")
	content.WriteString(a.renderDischargeRateChart(data.Measurements))
	content.WriteString("\n\n")
	
	// График температуры
	content.WriteString("🌡️ Температурный профиль\n")
	content.WriteString(a.renderTemperatureChart(data.Measurements))
	
	return content.String()
}

// renderChargeChart рендерит ASCII график заряда
func (a *App) renderChargeChart(measurements []Measurement) string {
	if len(measurements) == 0 {
		return "Нет данных для отображения"
	}
	
	// Берем последние 20 измерений для графика
	chartData := measurements
	if len(chartData) > 20 {
		chartData = chartData[len(chartData)-20:]
	}
	
	height := 10
	width := 50
	chart := make([][]string, height)
	for i := range chart {
		chart[i] = make([]string, width)
		for j := range chart[i] {
			chart[i][j] = " "
		}
	}
	
	// Находим min и max для масштабирования
	minVal, maxVal := 100, 0
	for _, m := range chartData {
		if m.Percentage < minVal {
			minVal = m.Percentage
		}
		if m.Percentage > maxVal {
			maxVal = m.Percentage
		}
	}
	
	// Добавляем отступ для лучшей визуализации
	if maxVal-minVal < 10 {
		minVal = max(0, minVal-5)
		maxVal = min(100, maxVal+5)
	}
	
	// Рисуем точки данных
	step := float64(width) / float64(len(chartData))
	for i, m := range chartData {
		x := int(float64(i) * step)
		if x >= width {
			x = width - 1
		}
		
		y := height - 1 - int(float64(m.Percentage-minVal)/float64(maxVal-minVal)*float64(height-1))
		if y < 0 {
			y = 0
		}
		if y >= height {
			y = height - 1
		}
		
		// Используем разные символы для визуализации
		if m.State == "charging" {
			chart[y][x] = "↑"
		} else if m.State == "discharging" {
			chart[y][x] = "↓"
		} else {
			chart[y][x] = "●"
		}
		
		// Соединяем точки линией
		if i > 0 {
			prevX := int(float64(i-1) * step)
			prevY := height - 1 - int(float64(chartData[i-1].Percentage-minVal)/float64(maxVal-minVal)*float64(height-1))
			
			// Простая линейная интерполяция
			if prevY != y {
				for j := 1; j < abs(y-prevY); j++ {
					midY := prevY
					if y > prevY {
						midY = prevY + j
					} else {
						midY = prevY - j
					}
					if midY >= 0 && midY < height {
						midX := prevX + (x-prevX)*j/abs(y-prevY)
						if midX >= 0 && midX < width && chart[midY][midX] == " " {
							chart[midY][midX] = "·"
						}
					}
				}
			}
		}
	}
	
	// Добавляем оси
	var result strings.Builder
	result.WriteString(fmt.Sprintf("%3d%% ┤", maxVal))
	for _, cell := range chart[0] {
		result.WriteString(cell)
	}
	result.WriteString("\n")
	
	for i := 1; i < height-1; i++ {
		result.WriteString("     │")
		for _, cell := range chart[i] {
			result.WriteString(cell)
		}
		result.WriteString("\n")
	}
	
	result.WriteString(fmt.Sprintf("%3d%% └", minVal))
	result.WriteString(strings.Repeat("─", width))
	result.WriteString("\n")
	result.WriteString("      ")
	result.WriteString(fmt.Sprintf("%-24s", chartData[0].Timestamp[11:16]))
	result.WriteString(fmt.Sprintf("%24s", chartData[len(chartData)-1].Timestamp[11:16]))
	
	return result.String()
}

// renderDischargeRateChart рендерит график скорости разряда
func (a *App) renderDischargeRateChart(measurements []Measurement) string {
	// Упрощенная версия sparkline графика
	if len(measurements) < 2 {
		return "Недостаточно данных"
	}
	
	sparkline := "▁▂▃▄▅▆▇█"
	var rates []float64
	
	for i := 1; i < len(measurements) && i < 20; i++ {
		if measurements[i].State == "discharging" && measurements[i-1].State == "discharging" {
			timeDiff := time.Since(time.Now()).Hours() // Заглушка, нужно парсить timestamp
			if timeDiff > 0 {
				rate := float64(measurements[i-1].Percentage-measurements[i].Percentage) / timeDiff
				rates = append(rates, rate)
			}
		}
	}
	
	if len(rates) == 0 {
		return "Нет данных о разряде"
	}
	
	// Находим min и max
	minRate, maxRate := rates[0], rates[0]
	for _, r := range rates {
		if r < minRate {
			minRate = r
		}
		if r > maxRate {
			maxRate = r
		}
	}
	
	var result strings.Builder
	for _, rate := range rates {
		idx := int((rate - minRate) / (maxRate - minRate) * float64(len(sparkline)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkline) {
			idx = len(sparkline) - 1
		}
		result.WriteString(string(sparkline[idx]))
	}
	
	result.WriteString(fmt.Sprintf("\nМин: %.1f%%/ч  Макс: %.1f%%/ч", minRate, maxRate))
	
	return result.String()
}

// renderTemperatureChart рендерит тепловую карту температуры
func (a *App) renderTemperatureChart(measurements []Measurement) string {
	if len(measurements) == 0 {
		return "Нет данных"
	}
	
	// Берем последние измерения
	data := measurements
	if len(data) > 30 {
		data = data[len(data)-30:]
	}
	
	var result strings.Builder
	
	// Создаем тепловую карту с цветами
	for _, m := range data {
		tempChar := "█"
		style := lipgloss.NewStyle()
		
		if m.Temperature < 25 {
			style = style.Foreground(lipgloss.Color("51")) // Холодный - голубой
		} else if m.Temperature < 35 {
			style = style.Foreground(lipgloss.Color("82")) // Нормальный - зеленый
		} else if m.Temperature < 45 {
			style = style.Foreground(lipgloss.Color("226")) // Теплый - желтый
		} else {
			style = style.Foreground(lipgloss.Color("196")) // Горячий - красный
		}
		
		result.WriteString(style.Render(tempChar))
	}
	
	result.WriteString("\n")
	result.WriteString(fmt.Sprintf("← %s", data[0].Timestamp[11:16]))
	result.WriteString(fmt.Sprintf(" → %s", data[len(data)-1].Timestamp[11:16]))
	result.WriteString("\n")
	result.WriteString("🧊 <25°C  ❄️ 25-35°C  🔥 35-45°C  🌋 >45°C")
	
	return result.String()
}

// renderReportAnomalies рендерит вкладку с аномалиями
func (a *App) renderReportAnomalies(data *ReportData) string {
	var content strings.Builder
	
	content.WriteString("⚠️ Анализ аномалий и проблем\n")
	content.WriteString(strings.Repeat("─", 50) + "\n\n")
	
	if len(data.Anomalies) == 0 {
		successStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")).
			Bold(true)
		content.WriteString(successStyle.Render("✅ Аномалий не обнаружено!\n\n"))
		content.WriteString("Батарея работает в штатном режиме.\n")
	} else {
		// Группируем аномалии по критичности
		critical := []string{}
		warning := []string{}
		info := []string{}
		
		for _, anomaly := range data.Anomalies {
			if strings.Contains(anomaly, "критич") || strings.Contains(anomaly, "опасн") {
				critical = append(critical, anomaly)
			} else if strings.Contains(anomaly, "вниман") || strings.Contains(anomaly, "высок") {
				warning = append(warning, anomaly)
			} else {
				info = append(info, anomaly)
			}
		}
		
		// Критические проблемы
		if len(critical) > 0 {
			criticalStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("196")).
				Bold(true)
			content.WriteString(criticalStyle.Render("🚨 Критические проблемы:\n"))
			for _, item := range critical {
				content.WriteString(fmt.Sprintf("  • %s\n", item))
			}
			content.WriteString("\n")
		}
		
		// Предупреждения
		if len(warning) > 0 {
			warningStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Bold(true)
			content.WriteString(warningStyle.Render("⚡ Требуют внимания:\n"))
			for _, item := range warning {
				content.WriteString(fmt.Sprintf("  • %s\n", item))
			}
			content.WriteString("\n")
		}
		
		// Информационные
		if len(info) > 0 {
			infoStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("226"))
			content.WriteString(infoStyle.Render("ℹ️ Информация:\n"))
			for _, item := range info {
				content.WriteString(fmt.Sprintf("  • %s\n", item))
			}
			content.WriteString("\n")
		}
	}
	
	// Рекомендации
	if len(data.Recommendations) > 0 {
		content.WriteString("\n💡 Рекомендации по улучшению:\n")
		content.WriteString(strings.Repeat("─", 40) + "\n")
		
		for i, rec := range data.Recommendations {
			content.WriteString(fmt.Sprintf("%d. %s\n", i+1, rec))
		}
	}
	
	// Добавляем инсайты на основе данных
	content.WriteString("\n\n📊 Статистика аномалий:\n")
	content.WriteString(fmt.Sprintf("• Обнаружено проблем: %d\n", len(data.Anomalies)))
	content.WriteString(fmt.Sprintf("• Рекомендаций: %d\n", len(data.Recommendations)))
	content.WriteString(fmt.Sprintf("• Валидных интервалов: %d\n", data.ValidIntervals))
	
	return content.String()
}

// renderReportHistory рендерит вкладку с историей
func (a *App) renderReportHistory(data *ReportData) string {
	var content strings.Builder
	
	content.WriteString("📜 История измерений\n")
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	// Показываем текущий фильтр
	filterStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("226")).
		Bold(true)
	content.WriteString(filterStyle.Render(fmt.Sprintf("Фильтр: %s | Сортировка: %s\n", 
		a.getFilterLabel(), a.getSortLabel())))
	content.WriteString("\n")
	
	// Фильтруем данные
	filtered := a.filterMeasurements(data.Measurements)
	
	// Сортируем данные
	sorted := a.sortMeasurements(filtered)
	
	// Обновляем таблицу
	a.updateHistoryTable(sorted)
	
	// Рендерим таблицу
	content.WriteString(a.report.historyTable.View())
	
	// Статистика
	content.WriteString("\n")
	statsStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))
	content.WriteString(statsStyle.Render(fmt.Sprintf(
		"Показано: %d из %d записей", 
		len(filtered), 
		len(data.Measurements),
	)))
	
	return content.String()
}

// filterMeasurements фильтрует измерения по состоянию
func (a *App) filterMeasurements(measurements []Measurement) []Measurement {
	if a.report.filterState == "all" {
		return measurements
	}
	
	var filtered []Measurement
	for _, m := range measurements {
		if m.State == a.report.filterState {
			filtered = append(filtered, m)
		}
	}
	
	return filtered
}

// sortMeasurements сортирует измерения
func (a *App) sortMeasurements(measurements []Measurement) []Measurement {
	// Создаем копию для сортировки
	sorted := make([]Measurement, len(measurements))
	copy(sorted, measurements)
	
	// Простая сортировка по времени
	if !a.report.sortDesc {
		// Обратный порядок (старые первые)
		for i := 0; i < len(sorted)/2; i++ {
			sorted[i], sorted[len(sorted)-1-i] = sorted[len(sorted)-1-i], sorted[i]
		}
	}
	
	return sorted
}

// updateHistoryTable обновляет данные в таблице истории
func (a *App) updateHistoryTable(measurements []Measurement) {
	var rows []table.Row
	
	count := 20 // Показываем последние 20 записей
	if len(measurements) < count {
		count = len(measurements)
	}
	
	for i := 0; i < count; i++ {
		m := measurements[i]
		
		// Форматируем данные для таблицы
		timeStr := m.Timestamp[11:19] // HH:MM:SS
		chargeStr := fmt.Sprintf("%d%%", m.Percentage)
		stateStr := formatBatteryStateShort(m.State)
		tempStr := fmt.Sprintf("%d°C", m.Temperature)
		
		// Вычисляем скорость разряда
		rateStr := "-"
		if i > 0 && measurements[i-1].State == "discharging" && m.State == "discharging" {
			rate := measurements[i-1].Percentage - m.Percentage
			if rate > 0 {
				rateStr = fmt.Sprintf("-%d%%/ч", rate)
			}
		}
		
		rows = append(rows, table.Row{
			timeStr,
			chargeStr,
			stateStr,
			tempStr,
			rateStr,
		})
	}
	
	a.report.historyTable.SetRows(rows)
}

// getFilterLabel возвращает метку текущего фильтра
func (a *App) getFilterLabel() string {
	switch a.report.filterState {
	case "all":
		return "Все"
	case "charging":
		return "Зарядка"
	case "discharging":
		return "Разрядка"
	default:
		return a.report.filterState
	}
}

// getSortLabel возвращает метку сортировки
func (a *App) getSortLabel() string {
	if a.report.sortDesc {
		return "Новые первые ↓"
	}
	return "Старые первые ↑"
}

// renderReportPredictions рендерит вкладку с прогнозами
func (a *App) renderReportPredictions(data *ReportData) string {
	var content strings.Builder
	
	content.WriteString("🔮 Прогнозы и аналитика\n")
	content.WriteString(strings.Repeat("─", 50) + "\n\n")
	
	// Прогноз времени работы
	if data.RemainingTime > 0 {
		timeStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")).
			Bold(true)
		content.WriteString(timeStyle.Render("⏱️ Прогноз времени работы:\n"))
		content.WriteString(fmt.Sprintf("• При текущей нагрузке: %s\n", formatDuration(data.RemainingTime)))
		
		// Дополнительные прогнозы
		lightUsage := time.Duration(float64(data.RemainingTime) * 1.5)
		heavyUsage := time.Duration(float64(data.RemainingTime) * 0.6)
		
		content.WriteString(fmt.Sprintf("• При легкой нагрузке: %s\n", formatDuration(lightUsage)))
		content.WriteString(fmt.Sprintf("• При тяжелой нагрузке: %s\n", formatDuration(heavyUsage)))
		content.WriteString("\n")
	}
	
	// Прогноз деградации
	content.WriteString("📉 Прогноз износа батареи:\n")
	
	// Рассчитываем прогноз на основе текущего износа и циклов
	currentWear := data.Wear
	currentCycles := data.Latest.CycleCount
	
	// Предполагаем 1 цикл в день в среднем
	cyclesPerMonth := 30
	wearPerCycle := currentWear / float64(max(currentCycles, 1))
	
	months := []int{1, 3, 6, 12}
	for _, m := range months {
		futureCycles := currentCycles + (cyclesPerMonth * m)
		futureWear := currentWear + (wearPerCycle * float64(cyclesPerMonth*m))
		
		wearStyle := lipgloss.NewStyle()
		if futureWear < 20 {
			wearStyle = wearStyle.Foreground(lipgloss.Color("82"))
		} else if futureWear < 30 {
			wearStyle = wearStyle.Foreground(lipgloss.Color("226"))
		} else {
			wearStyle = wearStyle.Foreground(lipgloss.Color("196"))
		}
		
		content.WriteString(fmt.Sprintf("• %s\n", 
			wearStyle.Render(fmt.Sprintf("Через %d мес: %.1f%% износа (%d циклов)", 
				m, futureWear, futureCycles))))
	}
	
	content.WriteString("\n")
	
	// Рекомендации по продлению срока службы
	content.WriteString("💡 Советы по продлению срока службы:\n")
	
	tips := []string{
		"Держите заряд в диапазоне 20-80% для минимального износа",
		"Избегайте полной разрядки батареи",
		"Используйте оригинальное зарядное устройство",
		"Избегайте перегрева (>45°C) и переохлаждения (<10°C)",
		"При длительной работе от сети извлекайте батарею (если возможно)",
	}
	
	for _, tip := range tips {
		content.WriteString(fmt.Sprintf("• %s\n", tip))
	}
	
	// Сравнение с эталонными показателями
	content.WriteString("\n📊 Сравнение с эталоном MacBook:\n")
	
	// Эталонные значения для MacBook
	benchmarkCycles := 1000
	benchmarkWear := 20.0
	
	cycleHealth := float64(benchmarkCycles-currentCycles) / float64(benchmarkCycles) * 100
	wearHealth := (benchmarkWear - currentWear) / benchmarkWear * 100
	
	if cycleHealth < 0 {
		cycleHealth = 0
	}
	if wearHealth < 0 {
		wearHealth = 0
	}
	
	content.WriteString(fmt.Sprintf("• Ресурс по циклам: %.0f%%\n", cycleHealth))
	content.WriteString(fmt.Sprintf("• Состояние по износу: %.0f%%\n", wearHealth))
	
	// Общая оценка
	overallHealth := (cycleHealth + wearHealth) / 2
	healthStyle := lipgloss.NewStyle().Bold(true)
	
	if overallHealth > 70 {
		healthStyle = healthStyle.Foreground(lipgloss.Color("82"))
		content.WriteString(healthStyle.Render("\n✅ Батарея в отличном состоянии!"))
	} else if overallHealth > 40 {
		healthStyle = healthStyle.Foreground(lipgloss.Color("226"))
		content.WriteString(healthStyle.Render("\n⚡ Батарея в хорошем состоянии"))
	} else {
		healthStyle = healthStyle.Foreground(lipgloss.Color("196"))
		content.WriteString(healthStyle.Render("\n⚠️ Рекомендуется замена батареи"))
	}
	
	return content.String()
}


// renderExport рендерит экран экспорта
func (a *App) renderExport() string {
	content := "📄 Экспорт отчетов\n\n"
	content += "Экспорт в HTML с автогенерацией имени файла\n\n"
	content += "Нажмите Enter для экспорта в HTML\n"
	content += "Файл будет сохранен в ~/Documents/ как batmon_report_YYYY-MM-DD.html\n\n"
	
	// Показываем статус экспорта если есть
	if a.exportStatus != "" {
		content += fmt.Sprintf("Статус: %s\n\n", a.exportStatus)
	}
	
	content += "Нажмите q для возврата в главное меню"
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1).
		Render(content)
}

// renderSettings рендерит экран очистки БД
func (a *App) renderSettings() string {
	content := "🗑️ Очистка базы данных\n\n"
	content += "⚠️  ВНИМАНИЕ: Эта операция удалит ВСЕ сохраненные данные!\n\n"
	content += "Будут удалены:\n"
	content += "• Все измерения батареи\n"
	content += "• История состояний\n"
	content += "• Статистика использования\n\n"
	content += "Нажмите Y для подтверждения очистки\n"
	content += "Нажмите q или N для отмены"
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1).
		Render(content)
}

// renderHelp рендерит экран справки
func (a *App) renderHelp() string {
	// Адаптируем размер к размеру терминала
	maxWidth := 70
	if a.windowWidth > 0 && a.windowWidth < 80 {
		maxWidth = a.windowWidth - 10
	}
	
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Align(lipgloss.Center).
		Render("🔋 Справка по BatMon") + "\n\n"
		
	// Основная цель
	purpose := lipgloss.NewStyle().
		Foreground(lipgloss.Color("10")).
		Bold(true).
		Render("🎯 ГЛАВНАЯ ЦЕЛЬ") + "\n"
	purpose += "Понять, нужно ли менять батарею MacBook\n\n"
	
	// Краткая инструкция
	howTo := lipgloss.NewStyle().
		Foreground(lipgloss.Color("12")).
		Bold(true).
		Render("🚀 КАК ПОЛЬЗОВАТЬСЯ") + "\n"
	howTo += "1. Зарядите до 100%\n"
	howTo += "2. Выберите '🔋 Полный анализ батареи'\n"
	howTo += "3. Разрядите до 0-10% (2-3 часа)\n"
	howTo += "4. Получите рекомендацию\n\n"
	
	// Режимы
	modes := lipgloss.NewStyle().
		Foreground(lipgloss.Color("11")).
		Bold(true).
		Render("📋 РЕЖИМЫ РАБОТЫ") + "\n"
	modes += "⚡ Быстрая диагностика - моментальная проверка\n"
	modes += "🔋 Полный анализ - основной тест (100%→0%)\n"
	modes += "📊 Детальный отчет - графики и тренды\n\n"
	
	// Критерии оценки
	criteria := lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render("🔍 ОЦЕНКА СОСТОЯНИЯ") + "\n"
	criteria += lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✅ Хорошо: ") + "износ <20%, циклы <1000\n"
	criteria += lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("⚠️  Внимание: ") + "износ 20-30%, циклы 1000+\n"
	criteria += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("🔴 Замена: ") + "износ >30%, циклы >1500\n\n"
	
	// Советы
	tips := lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("💡 СОВЕТЫ") + "\n"
	tips += "• Минимум 2-3 часа для точного анализа\n"
	tips += "• Не закрывайте программу во время теста\n"
	tips += "• MacBook не будет засыпать (кроме закрытия крышки)\n"
	tips += "• Сохраняйте отчеты для отслеживания\n\n"
	
	// Управление
	controls := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Align(lipgloss.Center).
		Render("Нажмите 'q' для выхода в главное меню")
	
	content := title + purpose + howTo + modes + criteria + tips + controls
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1).
		Width(maxWidth).
		Render(content)
}

// renderWelcome рендерит экран приветствия
func (a *App) renderWelcome() string {
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Align(lipgloss.Center).
		Render("🔋 BatMon v2.0") + "\n"
	
	subtitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("12")).
		Bold(true).
		Align(lipgloss.Center).
		Render("Интеллектуальный анализ батареи MacBook") + "\n\n"
		
	purpose := lipgloss.NewStyle().
		Foreground(lipgloss.Color("10")).
		Bold(true).
		Render("🎯 ЦЕЛЬ ПРОГРАММЫ") + "\n"
	purpose += "Помочь вам принять обоснованное решение:\n"
	purpose += lipgloss.NewStyle().
		Foreground(lipgloss.Color("11")).
		Bold(true).
		Render("НУЖНО ЛИ МЕНЯТЬ БАТАРЕЮ В ВАШЕМ MacBook?") + "\n\n"
	
	how := lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("🔍 КАК ЭТО РАБОТАЕТ") + "\n"
	how += "1. Программа собирает данные о работе батареи\n"
	how += "2. Анализирует реальные показатели vs. заявленные\n"  
	how += "3. Выявляет аномалии и проблемы\n"
	how += "4. Даёт чёткую рекомендацию с обоснованием\n\n"
	
	example := lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render("⚠️ ЗАЧЕМ ЭТО НУЖНО") + "\n"
	example += "Стандартные показатели macOS могут обманывать:\n"
	example += "• Батарея показывает 5 часов, а садится за 2 часа\n"
	example += "• Заряд резко проваливается с 90% до 40%\n"  
	example += "• Перегрев при обычной нагрузке\n\n"
	example += lipgloss.NewStyle().
		Foreground(lipgloss.Color("10")).
		Render("BatMon выявит такие проблемы и объяснит их причины!") + "\n\n"
	
	instruction := lipgloss.NewStyle().
		Foreground(lipgloss.Color("13")).
		Bold(true).
		Render("🚀 НАЧНЁМ!") + "\n"
	instruction += "Для максимально точного анализа:\n"
	instruction += "1. Зарядите MacBook до 100%\n"
	instruction += "2. Выберите 'Полный анализ батареи'\n"  
	instruction += "3. Используйте MacBook как обычно до разрядки\n"
	instruction += "4. MacBook не будет засыпать (кроме закрытия крышки)\n\n"
	
	controls := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Align(lipgloss.Center).
		Render("Нажмите Enter или Пробел для продолжения\n") +
		lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Align(lipgloss.Center).
		Render("'q' для выхода")
	
	content := title + subtitle + purpose + how + example + instruction + controls
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(2).
		Width(80).
		Align(lipgloss.Center).
		Render(content)
}

// renderQuickDiag рендерит быструю диагностику
func (a *App) renderQuickDiag() string {
	if a.latest == nil {
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("9")).
			Padding(2).
			Render("❌ Данные о батарее недоступны\n\nНажмите 'q' для выхода в меню")
	}
	
	wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
	healthStatus := getBatteryHealthStatus(wear, a.latest.CycleCount)
	healthColor := getBatteryHealthColor(wear, a.latest.CycleCount)
	
	// Заголовок
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Align(lipgloss.Center).
		Render("⚡ БЫСТРАЯ ДИАГНОСТИКА БАТАРЕИ") + "\n\n"
	
	// Основные показатели
	currentSection := lipgloss.NewStyle().
		Foreground(lipgloss.Color("12")).
		Bold(true).
		Render("📊 ТЕКУЩЕЕ СОСТОЯНИЕ") + "\n"
	
	currentSection += fmt.Sprintf("🔋 Заряд: %s\n", 
		lipgloss.NewStyle().
			Foreground(getBatteryColor(a.latest.Percentage)).
			Bold(true).
			Render(fmt.Sprintf("%d%%", a.latest.Percentage)))
	
	currentSection += fmt.Sprintf("🔄 Состояние: %s\n", formatBatteryState(a.latest.State))
	currentSection += fmt.Sprintf("🌡️ Температура: %s\n", 
		lipgloss.NewStyle().
			Foreground(getTemperatureColor(a.latest.Temperature)).
			Render(fmt.Sprintf("%d°C", a.latest.Temperature)))
	currentSection += "\n"
	
	// Здоровье батареи
	healthSection := lipgloss.NewStyle().
		Foreground(lipgloss.Color("10")).
		Bold(true).
		Render("💚 ЗДОРОВЬЕ БАТАРЕИ") + "\n"
	
	healthSection += fmt.Sprintf("📉 Износ: %s\n", 
		lipgloss.NewStyle().
			Foreground(getWearColor(wear)).
			Bold(true).
			Render(fmt.Sprintf("%.1f%%", wear)))
	
	healthSection += fmt.Sprintf("🔁 Циклы: %s\n", 
		lipgloss.NewStyle().
			Foreground(getCycleColor(a.latest.CycleCount)).
			Render(fmt.Sprintf("%d", a.latest.CycleCount)))
	
	healthSection += fmt.Sprintf("💚 Общая оценка: %s\n\n", 
		lipgloss.NewStyle().
			Foreground(lipgloss.Color(healthColor)).
			Bold(true).
			Render(healthStatus))
	
	// Быстрая рекомендация
	recommendationSection := lipgloss.NewStyle().
		Foreground(lipgloss.Color("11")).
		Bold(true).
		Render("🎯 БЫСТРАЯ РЕКОМЕНДАЦИЯ") + "\n"
	
	var recommendation string
	if wear < 20 && a.latest.CycleCount < 1000 {
		recommendation = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10")).
			Render("✅ Батарея в хорошем состоянии. Замена не требуется.")
	} else if wear < 30 && a.latest.CycleCount < 1500 {
		recommendation = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Render("⚠️ Батарея работает, но стоит планировать замену.")
	} else {
		recommendation = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Render("🔴 Рекомендуется замена батареи.")
	}
	recommendationSection += recommendation + "\n\n"
	
	// Дополнительные советы
	tipsSection := lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("💡 СОВЕТ") + "\n"
	tipsSection += "Для полного анализа выберите '🔋 Полный анализ батареи'\n"
	tipsSection += "или '📊 Детальный отчет' для графиков и трендов\n\n"
	
	// Управление
	controls := lipgloss.NewStyle().
		Foreground(lipgloss.Color("8")).
		Align(lipgloss.Center).
		Render("Нажмите 'q' для выхода в главное меню")
	
	content := title + currentSection + healthSection + recommendationSection + tipsSection + controls
	
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(2).
		Width(70).
		Render(content)
}

// initQuickDiag инициализирует быструю диагностику
func (a *App) initQuickDiag() {
	// Быстрая диагностика не требует специальной инициализации
	// Все данные берутся из текущего состояния
}

// initDashboard инициализирует dashboard
func (a *App) initDashboard() {
	// Создаем кастомные прогресс-бары с адаптивной шириной
	progressWidth := 30
	if a.windowWidth > 0 {
		progressWidth = (a.windowWidth / 2) - 20
		if progressWidth < 20 {
			progressWidth = 20
		}
		if progressWidth > 40 {
			progressWidth = 40
		}
	}
	
	batteryGauge := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(progressWidth),
	)
	
	wearGauge := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(progressWidth),
	)
	
	// Создаем таблицу с фиксированными колонками для компактности
	columns := []table.Column{
		{Title: "Время", Width: 5},
		{Title: "Заряд", Width: 5},
		{Title: "Состояние", Width: 10},
		{Title: "Темп.", Width: 5},
	}
	
	measureTable := table.New(
		table.WithColumns(columns),
		table.WithHeight(4), // Фиксированная высота для 4 записей
		table.WithFocused(false),
	)
	
	// Инициализация компонентов dashboard
	a.dashboard = DashboardModel{
		batteryGauge: batteryGauge,
		wearGauge:    wearGauge,
		measureTable: measureTable,
		lastUpdate:   time.Now(),
	}
}

// initReport инициализирует отчет
func (a *App) initReport() {
	// Инициализация вкладок
	tabs := []string{
		"📊 Обзор",
		"📈 Графики", 
		"⚠️ Аномалии",
		"📜 История",
		"🔮 Прогнозы",
	}
	
	// Создаем таблицу истории с адаптивными колонками
	tableWidth := a.windowWidth - 10
	if tableWidth < 50 {
		tableWidth = 50
	}
	columnWidths := a.calculateReportTableColumnWidths(tableWidth)
	
	columns := []table.Column{
		{Title: "Время", Width: columnWidths[0]},
		{Title: "Заряд", Width: columnWidths[1]},
		{Title: "Состояние", Width: columnWidths[2]},
		{Title: "Циклы", Width: columnWidths[3]},
		{Title: "Темп.", Width: columnWidths[4]},
		{Title: "Износ", Width: columnWidths[5]},
	}
	
	tableHeight := 15
	if a.windowHeight > 30 {
		tableHeight = min(20, a.windowHeight-10)
	}
	
	historyTable := table.New(
		table.WithColumns(columns),
		table.WithHeight(tableHeight),
		table.WithFocused(false),
	)
	
	a.report = ReportModel{
		viewHeight:   a.windowHeight - 4,
		tabs:         tabs,
		activeTab:    0,
		historyTable: historyTable,
		filterState:  "all",
		sortColumn:   0,
		sortDesc:     true,
		lastUpdate:   time.Now(),
	}
}

// updateDashboardData обновляет данные dashboard
func (a *App) updateDashboardData() {
	a.dashboard.lastUpdate = time.Now()
	a.dashboard.updating = false
}

// clearDatabase очищает всю базу данных
func (a *App) clearDatabase() error {
	// Останавливаем сервис сбора данных
	if a.dataService != nil {
		a.dataService.Stop()
		
		// Закрываем соединение с БД
		if a.dataService.db != nil {
			a.dataService.db.Close()
		}
	}
	
	// Удаляем файл базы данных и все связанные файлы
	dbPath := getDBPath()
	dbFiles := []string{
		dbPath,                // .batmon.sqlite
		dbPath + "-shm",       // .batmon.sqlite-shm
		dbPath + "-wal",       // .batmon.sqlite-wal
	}
	
	for _, file := range dbFiles {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			// Не возвращаем ошибку, если файл не существует
			// Продолжаем удаление других файлов
		}
	}
	
	// Очищаем буфер в памяти
	if a.dataService != nil && a.dataService.buffer != nil {
		a.dataService.buffer.measurements = make([]Measurement, 0)
	}
	
	// Очищаем локальные данные приложения
	a.measurements = make([]Measurement, 0)
	a.latest = nil
	
	// Переинициализируем базу данных и сервис
	db, err := initDB(getDBPath())
	if err != nil {
		return fmt.Errorf("не удалось переинициализировать БД: %v", err)
	}
	
	// Создаем новый буфер памяти
	buffer := NewMemoryBuffer(100) // Создаем буфер на 100 записей
	
	// Создаем новый сервис сбора данных
	a.dataService = NewDataService(db, buffer)
	a.dataService.Start()
	
	return nil
}
