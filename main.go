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
	"log"
	"os"
	"os/exec"
	"os/signal"
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
	lastProfilerCall time.Time
	pmsetInterval    time.Duration
	profilerInterval time.Duration
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
		temperature INTEGER DEFAULT 0
	);`
	if _, err = db.Exec(schema); err != nil {
		return nil, fmt.Errorf("создание таблицы: %w", err)
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
func parseSystemProfiler() (int, int, int, int, int, error) {
	cmd := exec.Command("system_profiler", "SPPowerDataType", "-detailLevel", "full")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("system_profiler: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	var cycle, fullCap, designCap, currCap, temperature int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Cycle Count:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "Cycle Count:"))
			cycle, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Full Charge Capacity:"):
			val := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Full Charge Capacity:")))[0]
			fullCap, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Design Capacity:"):
			val := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Design Capacity:")))[0]
			designCap, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Current Capacity:"):
			val := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Current Capacity:")))[0]
			currCap, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Temperature:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "Temperature:"))
			// Удаляем " C" в конце и конвертируем в целое число
			val = strings.Replace(val, " C", "", -1)
			temperature, _ = strconv.Atoi(val)
		}
	}
	if err = scanner.Err(); err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("сканирование system_profiler: %w", err)
	}
	return cycle, fullCap, designCap, currCap, temperature, nil
}

// getMeasurement собирает все данные о батарее и возвращает Measurement.
func getMeasurement() (*Measurement, error) {
	pct, state, pmErr := parsePMSet()
	if pmErr != nil {
		log.Printf("pmset: %v", pmErr)
	}
	cycle, fullCap, designCap, currCap, temperature, spErr := parseSystemProfiler()
	if spErr != nil {
		log.Printf("system_profiler: %v", spErr)
	}

	return &Measurement{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Percentage:      pct,
		State:           state,
		CycleCount:      cycle,
		FullChargeCap:   fullCap,
		DesignCapacity:  designCap,
		CurrentCapacity: currCap,
		Temperature:     temperature,
	}, nil
}

// insertMeasurement сохраняет Measurement в БД.
func insertMeasurement(db *sqlx.DB, m *Measurement) error {
	query := `INSERT INTO measurements (
		timestamp, percentage, state, cycle_count,
		full_charge_capacity, design_capacity, current_capacity, temperature)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := db.Exec(query,
		m.Timestamp, m.Percentage, m.State, m.CycleCount,
		m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity, m.Temperature)
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
func backgroundDataCollection(db *sqlx.DB, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	collector := &DataCollector{
		pmsetInterval:    pmsetInterval,
		profilerInterval: profilerInterval,
	}

	// Делаем первое измерение
	meas, err := getMeasurement()
	if err != nil {
		log.Printf("первичное измерение: %v", err)
		return
	}
	collector.lastProfilerCall = time.Now()

	if err = insertMeasurement(db, meas); err != nil {
		log.Printf("запись первой записи: %v", err)
	}

	ticker := time.NewTicker(collector.pmsetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Определяем, нужно ли вызывать system_profiler
			needProfiler := time.Since(collector.lastProfilerCall) >= collector.profilerInterval

			var m *Measurement
			if needProfiler {
				// Полное измерение с system_profiler
				m, err = getMeasurement()
				collector.lastProfilerCall = time.Now()
			} else {
				// Только pmset для быстрого обновления процента заряда
				pct, state, pmErr := parsePMSet()
				if pmErr != nil {
					log.Printf("pmset: %v", pmErr)
					continue
				}

				// Используем последние известные данные для остальных полей
				lastMs, dbErr := getLastNMeasurements(db, 1)
				if dbErr != nil || len(lastMs) == 0 {
					// Если нет предыдущих данных, делаем полное измерение
					m, err = getMeasurement()
					collector.lastProfilerCall = time.Now()
				} else {
					last := lastMs[0]
					m = &Measurement{
						Timestamp:       time.Now().UTC().Format(time.RFC3339),
						Percentage:      pct,
						State:           state,
						CycleCount:      last.CycleCount,
						FullChargeCap:   last.FullChargeCap,
						DesignCapacity:  last.DesignCapacity,
						CurrentCapacity: last.CurrentCapacity,
						Temperature:     last.Temperature,
					}
				}
			}

			if err != nil {
				log.Printf("измерение: %v", err)
				continue
			}
			if err = insertMeasurement(db, m); err != nil {
				log.Printf("запись измерения: %v", err)
			}

			// Если подключили зарядку или батарея села, можно замедлить сбор
			if strings.ToLower(m.State) == "charging" && m.Percentage >= 100 {
				log.Println("Батарея полностью заряжена, замедляем сбор данных")
				ticker.Reset(5 * time.Minute)
			} else if strings.ToLower(m.State) == "discharging" {
				// Возвращаем нормальный интервал при разрядке
				ticker.Reset(collector.pmsetInterval)
			}
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
		placeholder.Text = "Ожидание первых измерений батареи...\nДанные появятся через несколько секунд.\n\nНажмите 'q' для выхода"
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
				if e.ID == "q" || e.ID == "<C-c>" {
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

	// График заряда батареи
	batteryChart := widgets.NewPlot()
	batteryChart.Title = "Заряд батареи (%)"
	batteryChart.Data = make([][]float64, 1)
	batteryChart.Data[0] = make([]float64, len(measurements))
	for i, m := range measurements {
		batteryChart.Data[0][i] = float64(m.Percentage)
	}
	batteryChart.SetRect(0, 0, 60, 15)
	batteryChart.AxesColor = ui.ColorWhite
	batteryChart.LineColors[0] = ui.ColorGreen

	// График емкости
	capacityChart := widgets.NewPlot()
	capacityChart.Title = "Текущая емкость (мАч)"
	capacityChart.Data = make([][]float64, 1)
	capacityChart.Data[0] = make([]float64, len(measurements))
	for i, m := range measurements {
		capacityChart.Data[0][i] = float64(m.CurrentCapacity)
	}
	capacityChart.SetRect(60, 0, 120, 15)
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
		fmt.Sprintf("⏱️  Скорость: %.2f мАч/ч", robustRate),
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
		infoRows = append(infoRows, fmt.Sprintf("%s Температура: %d°C", tempEmoji, latest.Temperature))
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

	infoRows = append(infoRows, "", "Нажмите 'q' для выхода", "Нажмите 'r' для обновления")
	infoList.Rows = infoRows
	infoList.SetRect(0, 15, 60, 25)

	// Гистограмма состояний
	stateGauge := widgets.NewGauge()
	stateGauge.Title = "Заряд батареи"
	stateGauge.Percent = latest.Percentage
	stateGauge.SetRect(60, 15, 120, 18)
	stateGauge.BarColor = ui.ColorGreen
	if latest.Percentage < 20 {
		stateGauge.BarColor = ui.ColorRed
	} else if latest.Percentage < 50 {
		stateGauge.BarColor = ui.ColorYellow
	}

	// Износ батареи
	wearGauge := widgets.NewGauge()
	wearGauge.Title = "Износ батареи"
	wearGauge.Percent = int(wear)
	wearGauge.SetRect(60, 18, 120, 21)
	wearGauge.BarColor = ui.ColorRed

	// Таблица последних измерений
	table := widgets.NewTable()
	table.Title = "Последние измерения"
	table.Rows = [][]string{
		{"Время", "Заряд", "Состояние", "Емкость"},
	}
	for i := len(measurements) - 5; i < len(measurements) && i >= 0; i++ {
		if i < 0 {
			continue
		}
		m := measurements[i]
		timeStr := m.Timestamp[11:19] // только время
		table.Rows = append(table.Rows, []string{
			timeStr,
			fmt.Sprintf("%d%%", m.Percentage),
			m.State,
			fmt.Sprintf("%d мАч", m.CurrentCapacity),
		})
	}
	table.SetRect(60, 21, 120, 25)

	render := func() {
		ui.Render(batteryChart, capacityChart, infoList, stateGauge, wearGauge, table)
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
			switch e.ID {
			case "q", "<C-c>":
				return nil
			case "r":
				// Обновляем данные
				newMeasurements, err := getLastNMeasurements(db, 50)
				if err == nil && len(newMeasurements) > 0 {
					measurements = newMeasurements
					latest = measurements[len(measurements)-1]

					// Обновляем графики
					batteryChart.Data[0] = make([]float64, len(measurements))
					capacityChart.Data[0] = make([]float64, len(measurements))
					for i, m := range measurements {
						batteryChart.Data[0][i] = float64(m.Percentage)
						capacityChart.Data[0][i] = float64(m.CurrentCapacity)
					}

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
						fmt.Sprintf("⏱️  Скорость: %.2f мАч/ч", robustRate),
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
						infoRows = append(infoRows, fmt.Sprintf("%s Температура: %d°C", tempEmoji, latest.Temperature))
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

					infoRows = append(infoRows, "", "Нажмите 'q' для выхода", "Нажмите 'r' для обновления")
					infoList.Rows = infoRows

					render()
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

				// Обновляем все виджеты
				batteryChart.Data[0] = make([]float64, len(measurements))
				capacityChart.Data[0] = make([]float64, len(measurements))
				for i, m := range measurements {
					batteryChart.Data[0][i] = float64(m.Percentage)
					capacityChart.Data[0][i] = float64(m.CurrentCapacity)
				}

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
					fmt.Sprintf("⏱️  Скорость: %.2f мАч/ч", robustRate),
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
					infoRows = append(infoRows, fmt.Sprintf("%s Температура: %d°C", tempEmoji, latest.Temperature))
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

				infoRows = append(infoRows, "", "Нажмите 'q' для выхода", "Нажмите 'r' для обновления")
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
		printColoredStatus("🌡️  Температура", fmt.Sprintf("%d°C", latest.Temperature), tempLevel)
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
	// Убираем флаги - программа работает автоматически
	db, err := initDB(dbFile)
	if err != nil {
		log.Fatalf("инициализация БД: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\nПолучен сигнал завершения. Завершаю...")
		cancel()
	}()

	// Проверяем текущее состояние питания
	onBattery, state, percentage, err := isOnBattery()
	if err != nil {
		log.Printf("Ошибка определения состояния питания: %v", err)
		// Продолжаем работу, показываем что есть в базе
		if err := printReport(db); err != nil {
			log.Fatalf("вывод отчёта: %v", err)
		}
		return
	}

	fmt.Printf("⚡ Состояние питания: %s (%d%%)\n", formatStateWithEmoji(state, percentage), percentage)

	if onBattery {
		fmt.Println("Компьютер работает от батареи - запускаю мониторинг и дашборд...")

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

		// Ждем завершения фонового процесса
		cancel()
		wg.Wait()

	} else {
		fmt.Println("Компьютер работает от сети - показываю сохраненные данные...")

		// Просто показываем отчет по имеющимся данным
		if err := printReport(db); err != nil {
			log.Fatalf("вывод отчёта: %v", err)
		}
	}
}
