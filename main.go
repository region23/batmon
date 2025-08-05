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

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

const (
	dbFile   = "batmon.sqlite" // имя файла SQLite
	interval = 30 * time.Second
)

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
}

// initDB открывает соединение с SQLite и создаёт таблицу, если её нет.
func initDB(path string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("соединение с БД: %w", err)
	}
	schema := `CREATE TABLE IF NOT EXISTS measurements (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT NOT NULL,
		percentage INTEGER,
		state TEXT,
		cycle_count INTEGER,
		full_charge_capacity INTEGER,
		design_capacity INTEGER,
		current_capacity INTEGER
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
func parseSystemProfiler() (int, int, int, int, error) {
	cmd := exec.Command("system_profiler", "SPPowerDataType", "-detailLevel", "full")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("system_profiler: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	var cycle, fullCap, designCap, currCap int
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
		}
	}
	if err = scanner.Err(); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("сканирование system_profiler: %w", err)
	}
	return cycle, fullCap, designCap, currCap, nil
}

// getMeasurement собирает все данные о батарее и возвращает Measurement.
func getMeasurement() (*Measurement, error) {
	pct, state, pmErr := parsePMSet()
	if pmErr != nil {
		log.Printf("pmset: %v", pmErr)
	}
	cycle, fullCap, designCap, currCap, spErr := parseSystemProfiler()
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
	}, nil
}

// insertMeasurement сохраняет Measurement в БД.
func insertMeasurement(db *sqlx.DB, m *Measurement) error {
	query := `INSERT INTO measurements (
		timestamp, percentage, state, cycle_count,
		full_charge_capacity, design_capacity, current_capacity)
	VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := db.Exec(query,
		m.Timestamp, m.Percentage, m.State, m.CycleCount,
		m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity)
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

// detectBatteryAnomalies анализирует аномальные изменения заряда
func detectBatteryAnomalies(ms []Measurement) []string {
	if len(ms) < 2 {
		return nil
	}

	var anomalies []string

	for i := 0; i < len(ms)-1; i++ {
		prev := ms[i]
		curr := ms[i+1]

		// Резкий скачок заряда (больше 20% за один интервал)
		chargeDiff := curr.Percentage - prev.Percentage
		if chargeDiff > 20 {
			anomalies = append(anomalies, fmt.Sprintf("Резкий рост заряда: %d%% → %d%% (%s)",
				prev.Percentage, curr.Percentage, curr.Timestamp[11:19]))
		}

		// Резкое падение заряда (больше 20% за один интервал)
		if chargeDiff < -20 {
			anomalies = append(anomalies, fmt.Sprintf("Резкое падение заряда: %d%% → %d%% (%s)",
				prev.Percentage, curr.Percentage, curr.Timestamp[11:19]))
		}

		// Неожиданное изменение состояния
		if prev.State != curr.State {
			anomalies = append(anomalies, fmt.Sprintf("Смена состояния: %s → %s (%s)",
				prev.State, curr.State, curr.Timestamp[11:19]))
		}

		// Резкое изменение емкости (больше 500 мАч)
		capacityDiff := abs(curr.CurrentCapacity - prev.CurrentCapacity)
		if capacityDiff > 500 {
			anomalies = append(anomalies, fmt.Sprintf("Резкое изменение емкости: %d → %d мАч (%s)",
				prev.CurrentCapacity, curr.CurrentCapacity, curr.Timestamp[11:19]))
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

	analysis["health_status"] = healthStatus
	analysis["health_score"] = healthScore

	// Рекомендации
	var recommendations []string
	if wear > 20 {
		recommendations = append(recommendations, "Рассмотрите замену батареи")
	}
	if len(anomalies) > 3 {
		recommendations = append(recommendations, "Проверьте настройки энергосбережения")
	}
	if latest.CycleCount > 1000 {
		recommendations = append(recommendations, "Батарея приближается к концу жизненного цикла")
	}
	if avgRate > 1000 {
		recommendations = append(recommendations, "Высокое энергопотребление - закройте ресурсоемкие приложения")
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

// backgroundDataCollection запускает сбор данных в фоне
func backgroundDataCollection(db *sqlx.DB, ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	// Делаем первое измерение
	meas, err := getMeasurement()
	if err != nil {
		log.Printf("первичное измерение: %v", err)
		return
	}

	if err = insertMeasurement(db, meas); err != nil {
		log.Printf("запись первой записи: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m, err := getMeasurement()
			if err != nil {
				log.Printf("измерение: %v", err)
				continue
			}
			if err = insertMeasurement(db, m); err != nil {
				log.Printf("запись измерения: %v", err)
			}

			// Если подключили зарядку или батарея села, можно остановить сбор
			// Но для дашборда продолжаем работать
			if strings.ToLower(m.State) == "charging" && m.Percentage >= 100 {
				log.Println("Батарея полностью заряжена, замедляем сбор данных")
				// Увеличиваем интервал при полной зарядке
				ticker.Reset(5 * time.Minute)
			} else if strings.ToLower(m.State) == "discharging" {
				// Возвращаем нормальный интервал при разрядке
				ticker.Reset(interval)
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
		fmt.Sprintf("Заряд: %d%%", latest.Percentage),
		fmt.Sprintf("Состояние: %s", strings.Title(latest.State)),
		fmt.Sprintf("Циклы: %d", latest.CycleCount),
		fmt.Sprintf("Износ: %.1f%%", wear),
		fmt.Sprintf("Скорость: %.2f мАч/ч", robustRate),
		fmt.Sprintf("Время: %s", remaining.Truncate(time.Minute)),
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
						fmt.Sprintf("Заряд: %d%%", latest.Percentage),
						fmt.Sprintf("Состояние: %s", strings.Title(latest.State)),
						fmt.Sprintf("Циклы: %d", latest.CycleCount),
						fmt.Sprintf("Износ: %.1f%%", wear),
						fmt.Sprintf("Скорость: %.2f мАч/ч", robustRate),
						fmt.Sprintf("Время: %s", remaining.Truncate(time.Minute)),
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
					fmt.Sprintf("Заряд: %d%%", latest.Percentage),
					fmt.Sprintf("Состояние: %s", strings.Title(latest.State)),
					fmt.Sprintf("Циклы: %d", latest.CycleCount),
					fmt.Sprintf("Износ: %.1f%%", wear),
					fmt.Sprintf("Скорость: %.2f мАч/ч", robustRate),
					fmt.Sprintf("Время: %s", remaining.Truncate(time.Minute)),
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

// printReport выводит отчёт о последнем измерении и статистике.
func printReport(db *sqlx.DB) error {
	ms, err := getLastNMeasurements(db, 20) // Увеличиваем количество для лучшего анализа
	if err != nil {
		return fmt.Errorf("получение исторических данных: %w", err)
	}
	if len(ms) == 0 {
		fmt.Println("Нет записей для отчёта.")
		return nil
	}

	latest := ms[len(ms)-1]
	avgRate := computeAvgRate(ms, 5)
	robustRate, validIntervals := computeAvgRateRobust(ms, 10)
	remaining := computeRemainingTime(latest.CurrentCapacity, robustRate)
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)

	// Анализ здоровья батареи
	healthAnalysis := analyzeBatteryHealth(ms)

	fmt.Println("=== Текущее состояние батареи ===")
	fmt.Printf("%s | %d%% | %s\n", latest.Timestamp, latest.Percentage, strings.Title(latest.State))
	fmt.Printf("Состояние питания: %s\n", strings.Title(latest.State))
	fmt.Printf("Кол-во циклов: %d\n", latest.CycleCount)
	fmt.Printf("Полная ёмкость: %d мАч\n", latest.FullChargeCap)
	fmt.Printf("Дизайнерская ёмкость: %d мАч\n", latest.DesignCapacity)
	fmt.Printf("Текущая ёмкость: %d мАч\n", latest.CurrentCapacity)

	fmt.Println("\n=== Анализ здоровья батареи ===")
	if healthAnalysis != nil {
		fmt.Printf("Общее состояние: %s (оценка: %d/100)\n",
			healthAnalysis["health_status"], healthAnalysis["health_score"])
		fmt.Printf("Износ батареи: %.1f%%\n", wear)

		if anomalies, ok := healthAnalysis["anomalies"].([]string); ok && len(anomalies) > 0 {
			fmt.Printf("\n⚠️  Обнаружено аномалий за последние измерения: %d\n", len(anomalies))
			for i, anomaly := range anomalies {
				if i >= 5 { // Показываем максимум 5 последних аномалий
					fmt.Printf("... и еще %d\n", len(anomalies)-i)
					break
				}
				fmt.Printf("  • %s\n", anomaly)
			}
		}

		if recs, ok := healthAnalysis["recommendations"].([]string); ok && len(recs) > 0 {
			fmt.Println("\n💡 Рекомендации:")
			for _, rec := range recs {
				fmt.Printf("  • %s\n", rec)
			}
		}
	}

	fmt.Println("\n=== Статистика разрядки ===")
	if avgRate > 0 {
		fmt.Printf("Простая скорость разрядки: %.2f мАч/час\n", avgRate)
	}
	if robustRate > 0 {
		fmt.Printf("Робастная скорость разрядки: %.2f мАч/час (на основе %d валидных интервалов)\n",
			robustRate, validIntervals)
	} else {
		fmt.Println("Робастная скорость разрядки: недостаточно данных")
	}
	if remaining > 0 {
		fmt.Printf("Оставшееся время работы: %s\n", remaining.Truncate(time.Minute).String())
	} else {
		fmt.Println("Оставшееся время работы: неизвестно")
	}

	fmt.Println("\n=== Последние измерения (от старых к новым) ===")
	startIdx := 0
	if len(ms) > 10 {
		startIdx = len(ms) - 10 // Показываем последние 10
	}

	for i := startIdx; i < len(ms); i++ {
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

		fmt.Printf("%s%s | %d%% | %s | CC:%d | FC:%d | DC:%d | CurCap:%d\n",
			marker, m.Timestamp, m.Percentage, strings.Title(m.State),
			m.CycleCount, m.FullChargeCap, m.DesignCapacity, m.CurrentCapacity)
	}
	return nil
}

// watchLoop запускает непрерывный сбор данных с заданным интервалом.
func watchLoop(db *sqlx.DB, ctx context.Context) {
	meas, err := getMeasurement()
	if err != nil {
		log.Printf("первичное измерение: %v", err)
	} else if err = insertMeasurement(db, meas); err != nil {
		log.Printf("запись первой записи: %v", err)
	}

	if strings.ToLower(meas.State) == "charging" || meas.Percentage <= 0 {
		fmt.Println("\nБатарея полностью разряжена или подключено питание. Завершаю.")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nПолучен сигнал завершения. Завершаю...")
			return
		case <-ticker.C:
			m, err := getMeasurement()
			if err != nil {
				log.Printf("измерение: %v", err)
				continue
			}
			if err = insertMeasurement(db, m); err != nil {
				log.Printf("запись измерения: %v", err)
			}

			if strings.ToLower(m.State) == "charging" || m.Percentage <= 0 {
				fmt.Println("\nБатарея полностью разряжена или подключено питание. Завершаю.")
				return
			}
		}
	}
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

	fmt.Printf("Состояние питания: %s (%d%%)\n", strings.Title(state), percentage)

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
