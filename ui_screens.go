package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// renderWelcome рендерит экран приветствия
func (a *App) renderWelcome() string {
	var content strings.Builder
	
	// ASCII арт логотип
	logo := `
    ██████╗  █████╗ ████████╗███╗   ███╗ ██████╗ ███╗   ██╗
    ██╔══██╗██╔══██╗╚══██╔══╝████╗ ████║██╔═══██╗████╗  ██║
    ██████╔╝███████║   ██║   ██╔████╔██║██║   ██║██╔██╗ ██║
    ██╔══██╗██╔══██║   ██║   ██║╚██╔╝██║██║   ██║██║╚██╗██║
    ██████╔╝██║  ██║   ██║   ██║ ╚═╝ ██║╚██████╔╝██║ ╚████║
    ╚═════╝ ╚═╝  ╚═╝   ╚═╝   ╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝
`
	
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Render(logo))
	
	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Align(lipgloss.Center).
		Width(a.windowWidth).
		Render("Мониторинг батареи MacBook v2.0"))
	
	content.WriteString("\n\n")
	content.WriteString(lipgloss.NewStyle().
		Align(lipgloss.Center).
		Width(a.windowWidth).
		Render("🔋 Добро пожаловать в BatMon!"))
	
	content.WriteString("\n\n")
	content.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("246")).
		Align(lipgloss.Center).
		Width(a.windowWidth).
		Render("Нажмите Enter для продолжения или q для выхода"))
	
	return content.String()
}

// renderMenu рендерит главное меню
func (a *App) renderMenu() string {
	return lipgloss.NewStyle().
		Padding(1).
		Render(a.menu.list.View())
}

// renderQuickDiag рендерит быструю диагностику
func (a *App) renderQuickDiag() string {
	var content strings.Builder
	
	content.WriteString("⚡ Быстрая диагностика батареи\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	if a.latest == nil {
		content.WriteString("❌ Нет данных о батарее\n")
		content.WriteString("Нажмите 'q' для возврата в меню")
		return content.String()
	}
	
	// Основные показатели
	content.WriteString("📊 Основные показатели:\n")
	content.WriteString(fmt.Sprintf("   Заряд: %d%%\n", a.latest.Percentage))
	content.WriteString(fmt.Sprintf("   Состояние: %s\n", formatStateWithEmoji(a.latest.State, a.latest.Percentage)))
	
	if a.latest.CycleCount > 0 {
		content.WriteString(fmt.Sprintf("   Циклы: %d\n", a.latest.CycleCount))
	}
	
	if a.latest.Temperature > 0 {
		tempStatus := "Нормальная"
		if a.latest.Temperature > 40 {
			tempStatus = "Высокая ⚠️"
		}
		content.WriteString(fmt.Sprintf("   Температура: %d°C (%s)\n", a.latest.Temperature, tempStatus))
	}
	
	// Анализ здоровья
	content.WriteString("\n💊 Здоровье батареи:\n")
	
	if a.latest.FullChargeCap > 0 && a.latest.DesignCapacity > 0 {
		wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
		wearStatus := "Отличное"
		if wear > 20 {
			wearStatus = "Требует внимания ⚠️"
		} else if wear > 10 {
			wearStatus = "Удовлетворительное"
		}
		
		content.WriteString(fmt.Sprintf("   Износ: %.1f%% (%s)\n", wear, wearStatus))
		content.WriteString(fmt.Sprintf("   Емкость: %d/%d мАч\n", a.latest.FullChargeCap, a.latest.DesignCapacity))
	}
	
	// Рекомендации
	content.WriteString("\n💡 Рекомендации:\n")
	
	recommendations := []string{}
	
	if a.latest.Percentage < 20 && a.latest.State != "charging" {
		recommendations = append(recommendations, "Подключите зарядное устройство")
	}
	
	if a.latest.Temperature > 40 {
		recommendations = append(recommendations, "Снизьте нагрузку на систему")
	}
	
	if a.latest.FullChargeCap > 0 && a.latest.DesignCapacity > 0 {
		wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
		if wear > 20 {
			recommendations = append(recommendations, "Рассмотрите замену батареи")
		}
	}
	
	if len(recommendations) == 0 {
		recommendations = append(recommendations, "Батарея работает нормально ✅")
	}
	
	for i, rec := range recommendations {
		content.WriteString(fmt.Sprintf("   %d. %s\n", i+1, rec))
	}
	
	content.WriteString("\n")
	content.WriteString("Нажмите 'q' для возврата в меню")
	
	return content.String()
}

// renderExport рендерит экран экспорта
func (a *App) renderExport() string {
	var content strings.Builder
	
	content.WriteString("📄 Экспорт отчетов\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	content.WriteString("Экспорт данных о батарее в HTML формат\n\n")
	
	content.WriteString("📁 Файл будет сохранен в папку Documents\n")
	content.WriteString("📅 Имя файла: batmon_report_" + time.Now().Format("2006-01-02") + ".html\n\n")
	
	if a.exportStatus != "" {
		statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("green"))
		if strings.Contains(a.exportStatus, "Ошибка") {
			statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("red"))
		}
		
		content.WriteString("Статус: " + statusStyle.Render(a.exportStatus) + "\n\n")
	}
	
	content.WriteString("⌨️  Управление:\n")
	content.WriteString("   Enter - Начать экспорт\n")
	content.WriteString("   q - Назад в меню\n")
	
	return content.String()
}

// renderSettings рендерит экран настроек
func (a *App) renderSettings() string {
	var content strings.Builder
	
	content.WriteString("🗑️  Очистка данных\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	content.WriteString("⚠️  Внимание! Эта операция удалит ВСЕ сохраненные данные:\n\n")
	content.WriteString("   • Все измерения батареи\n")
	content.WriteString("   • Историю зарядки\n")
	content.WriteString("   • Аналитические данные\n")
	content.WriteString("   • Файл базы данных\n\n")
	
	if a.lastError != nil {
		content.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color("red")).
			Render(fmt.Sprintf("❌ Ошибка: %v\n\n", a.lastError)))
	}
	
	content.WriteString("Вы уверены что хотите продолжить?\n\n")
	content.WriteString("⌨️  Управление:\n")
	content.WriteString("   Y/Д - Да, очистить все данные\n")
	content.WriteString("   N/Н или q - Нет, вернуться в меню\n")
	
	return content.String()
}

// renderHelp рендерит экран справки
func (a *App) renderHelp() string {
	var content strings.Builder
	
	content.WriteString("❓ Справка BatMon v2.0\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	content.WriteString("🔋 О программе:\n")
	content.WriteString("BatMon - это продвинутая утилита для мониторинга состояния\n")
	content.WriteString("батареи MacBook. Поддерживает интерактивный мониторинг,\n")
	content.WriteString("детальную аналитику и экспорт отчетов.\n\n")
	
	content.WriteString("📊 Возможности:\n")
	content.WriteString("• Интерактивный дашборд с графиками\n")
	content.WriteString("• Анализ трендов и прогноз деградации\n")
	content.WriteString("• Мониторинг температуры и расширенных метрик\n")
	content.WriteString("• Экспорт в HTML формат\n")
	content.WriteString("• Автоматическая ретенция данных\n")
	content.WriteString("• Цветной вывод и эмодзи индикаторы\n\n")
	
	content.WriteString("⌨️  Горячие клавиши:\n")
	content.WriteString("q/Q - Выход из приложения\n")
	content.WriteString("h/H/? - Показать справку\n")
	content.WriteString("d/D - Дашборд (главный экран)\n")
	content.WriteString("r/R - Отчеты и аналитика\n")
	content.WriteString("e/E - Экспорт данных\n")
	content.WriteString("s/S - Настройки\n")
	content.WriteString("↑↓ - Навигация по меню\n")
	content.WriteString("Enter - Выбор пункта меню\n")
	content.WriteString("Esc - Назад/отмена\n\n")
	
	content.WriteString("🚀 Командная строка:\n")
	content.WriteString("batmon -export-html <файл> - Экспорт в HTML\n")
	content.WriteString("batmon -version           - Показать версию\n")
	content.WriteString("batmon -help              - Показать справку\n\n")
	
	content.WriteString("⚠️  Примечания:\n")
	content.WriteString("• Приложение требует macOS для работы с батареей\n")
	content.WriteString("• Данные сохраняются в ~/.local/share/batmon/\n")
	content.WriteString("• Для точных показаний используется pmset и ioreg\n\n")
	
	content.WriteString("Нажмите 'q' для возврата в меню")
	
	return content.String()
}

// renderReport рендерит экран отчетов (заглушка)
func (a *App) renderReport() string {
	var content strings.Builder
	
	content.WriteString("📊 Детальные отчеты\n")
	content.WriteString(strings.Repeat("═", 50) + "\n\n")
	
	content.WriteString("🚧 Раздел в разработке\n\n")
	content.WriteString("В будущих версиях здесь будут доступны:\n")
	content.WriteString("• Подробная аналитика батареи\n")
	content.WriteString("• Графики изменения емкости\n")
	content.WriteString("• Анализ циклов зарядки\n")
	content.WriteString("• Прогнозы деградации\n")
	content.WriteString("• История аномалий\n\n")
	
	content.WriteString("Нажмите 'q' для возврата в меню")
	
	return content.String()
}