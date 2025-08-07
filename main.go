package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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
				log.Println("❌ Укажите имя файла для экспорта")
				return
			}
			if err := runExportMode(os.Args[2], "", true); err != nil {
				log.Fatalf("❌ Ошибка экспорта: %v", err)
			}
			return
		case "-export-html", "--export-html":
			if len(os.Args) < 3 {
				log.Println("❌ Укажите имя файла для экспорта")
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
	
	// Инициализация меню
	app := &App{
		state:       StateWelcome,
		dataService: dataService,
	}
	
	app.initMenu()
	
	return app
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