package main

import (
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
)

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

// updateMenu обрабатывает события в меню
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

// updateDashboard обрабатывает события в дашборде
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
		if a.report.activeTab < 3 { // 4 вкладки всего (0-3)
			a.report.activeTab++
			a.reportScrollY = 0
		}
	case "1":
		a.report.activeTab = 0
		a.reportScrollY = 0
	case "2":
		a.report.activeTab = 1
		a.reportScrollY = 0
	case "3":
		a.report.activeTab = 2
		a.reportScrollY = 0
	case "4":
		a.report.activeTab = 3
		a.reportScrollY = 0
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

// updateExport обрабатывает события в режиме экспорта
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

// updateSettings обрабатывает события в настройках
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
			progress.WithScaledGradient("#00ff00", "#ff0000"),
			progress.WithWidth(progressWidth),
		)
	}
}

// updateDashboardData обновляет данные дашборда
func (a *App) updateDashboardData() {
	a.dashboard.lastUpdate = time.Now()
	a.dashboard.updating = false
}

// calculateMaxDashboardScroll рассчитывает максимальный скролл для дашборда
func (a *App) calculateMaxDashboardScroll() int {
	// Примерное количество строк в дашборде
	baseLines := 35 // базовое количество строк
	if len(a.measurements) > 10 {
		baseLines += (len(a.measurements) - 10) / 5 // добавляем по строке за каждые 5 измерений
	}
	
	maxScroll := baseLines - (a.windowHeight - 5)
	if maxScroll < 0 {
		return 0
	}
	return maxScroll
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
		
		// Выполняем экспорт
		err = runExportMode("", filename, true)
		if err != nil {
			a.exportStatus = fmt.Sprintf("Ошибка экспорта: %v", err)
		} else {
			a.exportStatus = fmt.Sprintf("✅ Экспорт завершен: %s", filename)
		}
	}()
}

// initDashboard инициализирует дашборд
func (a *App) initDashboard() {
	a.dashboard.batteryGauge = progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(30),
	)
	a.dashboard.wearGauge = progress.New(
		progress.WithScaledGradient("#00ff00", "#ff0000"),
		progress.WithWidth(30),
	)
	a.dashboard.lastUpdate = time.Now()
	a.dashboard.updating = false
	a.dashboardScrollY = 0
}

// initReport инициализирует отчет
func (a *App) initReport() {
	a.report.activeTab = 0
	a.reportScrollY = 0
}

// initQuickDiag инициализирует быструю диагностику
func (a *App) initQuickDiag() {
	// Инициализация не требуется для быстрой диагностики
}

// initMenu инициализирует главное меню
func (a *App) initMenu() {
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
	
	a.menu.list = menuList
}