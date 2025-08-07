package main

import (
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/jmoiron/sqlx"
)

// DatabaseInterface определяет интерфейс для базы данных
type DatabaseInterface = *sqlx.DB

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

// ReportData содержит все данные для генерации отчета
type ReportData struct {
	GeneratedAt     time.Time
	Version         string
	Latest          Measurement
	Measurements    []Measurement
	HealthAnalysis  map[string]interface{}
	HealthStatus    string
	WearLevel       float64
	Wear            float64
	AvgRate         float64
	RobustRate      float64
	ValidIntervals  int
	RemainingTime   time.Duration
	Anomalies       []string
	Recommendations []string
	ChargeCycles    []ChargeCycle
	TrendAnalysis   TrendAnalysis
	AdvancedMetrics AdvancedMetrics
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

// UI модели
type MenuModel struct {
	list   list.Model
	choice string
}

type ChartModel struct {
	title string
	data  []float64
}

type InfoListModel struct {
	items []InfoItem
}

type InfoItem struct {
	label string
	value string
	icon  string
}

type ReportWidget struct {
	title      string
	widgetType string
	value      float64
	maxValue   float64
	color      lipgloss.Color
	icon       string
}

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

// menuItem для Bubble Tea list
type menuItem struct {
	title, desc string
}

func (i menuItem) FilterValue() string { return i.title }
func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }

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

// Сообщения Bubble Tea
type tickMsg time.Time
type dataUpdateMsg struct {
	measurements []Measurement
	latest       *Measurement
}

type errorMsg struct{ err error }