// main.go
//
// Консольная утилита batmon – мониторинг и оценка состояния батареи MacBook (Apple Silicon).
// Считывает данные о аккумуляторе, сохраняет их в SQLite и выводит отчёт.

package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

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

// printReport выводит отчёт о последнем измерении и статистике.
func printReport(db *sqlx.DB) error {
	ms, err := getLastNMeasurements(db, 10)
	if err != nil {
		return fmt.Errorf("получение исторических данных: %w", err)
	}
	if len(ms) == 0 {
		fmt.Println("Нет записей для отчёта.")
		return nil
	}

	latest := ms[len(ms)-1]
	avgRate := computeAvgRate(ms, 5)
	remaining := computeRemainingTime(latest.CurrentCapacity, avgRate)
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)

	fmt.Println("=== Текущее состояние батареи ===")
	fmt.Printf("%s | %d%% | %s\n", latest.Timestamp, latest.Percentage, strings.Title(latest.State))
	fmt.Printf("Состояние питания: %s\n", strings.Title(latest.State))
	fmt.Printf("Кол-во циклов: %d\n", latest.CycleCount)
	fmt.Printf("Полная ёмкость: %d мАч\n", latest.FullChargeCap)
	fmt.Printf("Дизайнерская ёмкость: %d мАч\n", latest.DesignCapacity)
	fmt.Printf("Текущая ёмкость: %d мАч\n", latest.CurrentCapacity)

	fmt.Println("\n=== Статистика за последние измерения ===")
	if avgRate > 0 {
		fmt.Printf("Средняя скорость разрядки (за 5 интервалов): %.2f мАч/час\n", avgRate)
	} else {
		fmt.Println("Средняя скорость разрядки: неизвестно")
	}
	if remaining > 0 {
		fmt.Printf("Оставшееся время работы: %s\n", remaining.Truncate(time.Minute).String())
	} else {
		fmt.Println("Оставшееся время работы: неизвестно")
	}
	fmt.Printf("Износ батареи: %.1f%%\n", wear)

	fmt.Println("\n=== Последние измерения (от старых к новым) ===")
	for _, m := range ms {
		fmt.Printf("%s | %d%% | %s | CC:%d | FC:%d | DC:%d | CurCap:%d\n",
			m.Timestamp, m.Percentage, strings.Title(m.State),
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
	var watchMode bool
	flag.BoolVar(&watchMode, "watch", false, "режим непрерывного сбора данных")
	flag.Parse()

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

	if watchMode {
		watchLoop(db, ctx)
	} else {
		if err := printReport(db); err != nil {
			log.Fatalf("вывод отчёта: %v", err)
		}
	}
}
