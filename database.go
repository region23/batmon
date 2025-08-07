package main

import (
	"fmt"
	"log"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

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