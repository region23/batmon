package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

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

// renderFullDashboard рендерит полный дашборд
func (a *App) renderFullDashboard(contentWidth, contentHeight int) string {
	var content strings.Builder
	
	// Заголовок
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Align(lipgloss.Center).
		Width(contentWidth).
		Render("🔋 BatMon Dashboard")
	
	content.WriteString(title)
	content.WriteString("\n\n")
	
	// Основная информация о батарее
	content.WriteString(a.renderMainBatteryInfo())
	content.WriteString("\n")
	
	// Прогресс-бары
	content.WriteString(a.renderProgressBars())
	content.WriteString("\n")
	
	// Расширенная информация
	content.WriteString(a.renderExtendedInfo())
	content.WriteString("\n")
	
	// Мини-график последних измерений
	if len(a.measurements) > 0 {
		content.WriteString(a.renderMiniChart())
		content.WriteString("\n")
	}
	
	// Статистика и рекомендации
	content.WriteString(a.renderStatsAndRecommendations())
	content.WriteString("\n")
	
	// Панель управления
	content.WriteString(a.renderDashboardControls())
	
	return content.String()
}

// renderCompactDashboard рендерит компактную версию дашборда
func (a *App) renderCompactDashboard() string {
	var content strings.Builder
	
	// Компактный заголовок
	content.WriteString("🔋 BatMon\n")
	content.WriteString(fmt.Sprintf("Заряд: %d%% | %s\n", 
		a.latest.Percentage, 
		formatStateWithEmoji(a.latest.State, a.latest.Percentage)))
	
	if a.latest.FullChargeCap > 0 && a.latest.DesignCapacity > 0 {
		wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
		content.WriteString(fmt.Sprintf("Износ: %.1f%%\n", wear))
	}
	
	if a.latest.Temperature > 0 {
		content.WriteString(fmt.Sprintf("Температура: %d°C\n", a.latest.Temperature))
	}
	
	content.WriteString("\nНажмите 'q' для выхода")
	
	return content.String()
}

// renderLoadingScreen рендерит экран загрузки
func (a *App) renderLoadingScreen() string {
	var content strings.Builder
	
	content.WriteString("🔋 BatMon Dashboard\n\n")
	content.WriteString("⏳ Загрузка данных о батарее...")
	
	if a.dashboard.updating {
		content.WriteString(" [Обновление]")
	}
	
	content.WriteString("\n\n")
	content.WriteString("Ожидание данных от системы...\n")
	content.WriteString("Если загрузка затянулась, нажмите 'r' для обновления")
	
	return content.String()
}

// renderMainBatteryInfo рендерит основную информацию о батарее
func (a *App) renderMainBatteryInfo() string {
	var content strings.Builder
	
	// Основная строка с зарядом
	batteryIcon := "🔋"
	if a.latest.Percentage < 20 {
		batteryIcon = "🪫"
	} else if a.latest.State == "charging" {
		batteryIcon = "⚡"
	}
	
	mainInfo := fmt.Sprintf("%s %d%% • %s", 
		batteryIcon, 
		a.latest.Percentage,
		formatStateWithEmoji(a.latest.State, a.latest.Percentage))
	
	content.WriteString(lipgloss.NewStyle().
		Bold(true).
		Foreground(getBatteryColor(a.latest.Percentage)).
		Render(mainInfo))
	
	content.WriteString("\n")
	
	// Дополнительная информация в две колонки
	leftColumn := []string{}
	rightColumn := []string{}
	
	if a.latest.CycleCount > 0 {
		leftColumn = append(leftColumn, fmt.Sprintf("Циклы: %d", a.latest.CycleCount))
	}
	
	if a.latest.FullChargeCap > 0 {
		rightColumn = append(rightColumn, fmt.Sprintf("Емкость: %d мАч", a.latest.FullChargeCap))
	}
	
	if a.latest.Temperature > 0 {
		tempColor := "green"
		if a.latest.Temperature > 35 {
			tempColor = "yellow"
		}
		if a.latest.Temperature > 45 {
			tempColor = "red"
		}
		
		leftColumn = append(leftColumn, 
			lipgloss.NewStyle().
				Foreground(lipgloss.Color(tempColor)).
				Render(fmt.Sprintf("Температура: %d°C", a.latest.Temperature)))
	}
	
	if a.latest.Voltage > 0 {
		rightColumn = append(rightColumn, fmt.Sprintf("Напряжение: %d мВ", a.latest.Voltage))
	}
	
	// Рендерим колонки
	maxLines := max(len(leftColumn), len(rightColumn))
	for i := 0; i < maxLines; i++ {
		left := ""
		right := ""
		
		if i < len(leftColumn) {
			left = leftColumn[i]
		}
		if i < len(rightColumn) {
			right = rightColumn[i]
		}
		
		content.WriteString(fmt.Sprintf("%-30s %s\n", left, right))
	}
	
	return content.String()
}

// renderProgressBars рендерит прогресс-бары
func (a *App) renderProgressBars() string {
	var content strings.Builder
	
	// Прогресс-бар заряда
	batteryPercent := float64(a.latest.Percentage) / 100.0
	content.WriteString("Заряд батареи:\n")
	content.WriteString(a.dashboard.batteryGauge.ViewAs(batteryPercent))
	content.WriteString(fmt.Sprintf(" %d%%\n", a.latest.Percentage))
	
	// Прогресс-бар износа
	if a.latest.FullChargeCap > 0 && a.latest.DesignCapacity > 0 {
		wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
		wearPercent := wear / 30.0 // Максимум 30% износа
		if wearPercent > 1.0 {
			wearPercent = 1.0
		}
		
		content.WriteString("\nИзнос батареи:\n")
		content.WriteString(a.dashboard.wearGauge.ViewAs(wearPercent))
		content.WriteString(fmt.Sprintf(" %.1f%%\n", wear))
	}
	
	return content.String()
}

// renderExtendedInfo рендерит расширенную информацию
func (a *App) renderExtendedInfo() string {
	var content strings.Builder
	
	content.WriteString("📊 Детальная информация\n")
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	// Информация о емкости
	if a.latest.DesignCapacity > 0 {
		content.WriteString(fmt.Sprintf("Проектная емкость: %d мАч\n", a.latest.DesignCapacity))
	}
	
	if a.latest.CurrentCapacity > 0 {
		content.WriteString(fmt.Sprintf("Текущая емкость: %d мАч\n", a.latest.CurrentCapacity))
	}
	
	// Информация о питании
	if a.latest.Power != 0 {
		powerStr := fmt.Sprintf("%.1f Вт", float64(a.latest.Power)/1000.0)
		if a.latest.Power > 0 {
			powerStr = "+" + powerStr + " (зарядка)"
		} else {
			powerStr = powerStr + " (разрядка)"
		}
		content.WriteString(fmt.Sprintf("Мощность: %s\n", powerStr))
	}
	
	if a.latest.Amperage != 0 {
		content.WriteString(fmt.Sprintf("Ток: %d мА\n", a.latest.Amperage))
	}
	
	// Состояние от Apple
	if a.latest.AppleCondition != "" {
		conditionColor := "green"
		icon := "✅"
		
		condition := strings.ToLower(a.latest.AppleCondition)
		if strings.Contains(condition, "service") || strings.Contains(condition, "replace") {
			conditionColor = "red"
			icon = "⚠️"
		} else if strings.Contains(condition, "fair") {
			conditionColor = "yellow"
			icon = "⚠️"
		}
		
		content.WriteString(fmt.Sprintf("Состояние: %s %s\n", 
			icon,
			lipgloss.NewStyle().
				Foreground(lipgloss.Color(conditionColor)).
				Render(a.latest.AppleCondition)))
	}
	
	// Время последнего обновления
	if !a.dashboard.lastUpdate.IsZero() {
		content.WriteString(fmt.Sprintf("\nПоследнее обновление: %s\n", 
			a.dashboard.lastUpdate.Format("15:04:05")))
	}
	
	return content.String()
}

// renderMiniChart рендерит мини-график последних измерений
func (a *App) renderMiniChart() string {
	var content strings.Builder
	
	content.WriteString("📈 История заряда (последние 20 измерений)\n")
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	// Берем последние 20 измерений
	measurements := a.measurements
	if len(measurements) > 20 {
		measurements = measurements[len(measurements)-20:]
	}
	
	if len(measurements) < 2 {
		content.WriteString("Недостаточно данных для отображения графика\n")
		return content.String()
	}
	
	// Простой ASCII график
	chart := a.renderASCIIChart(measurements, 40, 8)
	content.WriteString(chart)
	
	return content.String()
}

// renderASCIIChart рендерит простой ASCII график
func (a *App) renderASCIIChart(measurements []Measurement, width, height int) string {
	if len(measurements) < 2 {
		return "Недостаточно данных\n"
	}
	
	var content strings.Builder
	
	// Находим min/max значения
	minVal, maxVal := measurements[0].Percentage, measurements[0].Percentage
	for _, m := range measurements {
		if m.Percentage < minVal {
			minVal = m.Percentage
		}
		if m.Percentage > maxVal {
			maxVal = m.Percentage
		}
	}
	
	// Избегаем деления на ноль
	if maxVal == minVal {
		maxVal = minVal + 1
	}
	
	// Строим график построчно
	for y := height - 1; y >= 0; y-- {
		line := ""
		threshold := minVal + (maxVal-minVal)*y/height
		
		for i := 0; i < width && i < len(measurements); i++ {
			measurementIndex := i * len(measurements) / width
			if measurementIndex >= len(measurements) {
				measurementIndex = len(measurements) - 1
			}
			
			value := measurements[measurementIndex].Percentage
			if value >= threshold {
				line += "█"
			} else if value >= threshold-((maxVal-minVal)/height/2) {
				line += "▄"
			} else {
				line += " "
			}
		}
		
		// Добавляем шкалу
		label := fmt.Sprintf("%3d%%", threshold)
		if y == height-1 {
			label = fmt.Sprintf("%3d%%", maxVal)
		} else if y == 0 {
			label = fmt.Sprintf("%3d%%", minVal)
		}
		
		content.WriteString(fmt.Sprintf("%s │%s\n", label, line))
	}
	
	// Временная ось
	content.WriteString("     └" + strings.Repeat("─", width) + "\n")
	
	// Метки времени
	if len(measurements) >= 2 {
		first, _ := time.Parse(time.RFC3339, measurements[0].Timestamp)
		last, _ := time.Parse(time.RFC3339, measurements[len(measurements)-1].Timestamp)
		
		timeRange := fmt.Sprintf("      %s → %s", 
			first.Format("15:04"), 
			last.Format("15:04"))
		content.WriteString(timeRange + "\n")
	}
	
	return content.String()
}

// renderStatsAndRecommendations рендерит статистику и рекомендации
func (a *App) renderStatsAndRecommendations() string {
	var content strings.Builder
	
	content.WriteString("💡 Быстрые рекомендации\n")
	content.WriteString(strings.Repeat("─", 50) + "\n")
	
	recommendations := []string{}
	
	// Рекомендации по заряду
	if a.latest.Percentage < 20 && a.latest.State != "charging" {
		recommendations = append(recommendations, "🔌 Подключите зарядное устройство")
	} else if a.latest.Percentage == 100 && a.latest.State == "charging" {
		recommendations = append(recommendations, "🔋 Отключите зарядку для продления срока службы")
	}
	
	// Рекомендации по температуре
	if a.latest.Temperature > 40 {
		recommendations = append(recommendations, "🌡️ Высокая температура - закройте ресурсоемкие приложения")
	}
	
	// Рекомендации по износу
	if a.latest.FullChargeCap > 0 && a.latest.DesignCapacity > 0 {
		wear := computeWear(a.latest.DesignCapacity, a.latest.FullChargeCap)
		if wear > 20 {
			recommendations = append(recommendations, "⚠️ Высокий износ - рассмотрите замену батареи")
		}
	}
	
	// Рекомендации по циклам
	if a.latest.CycleCount > 800 {
		recommendations = append(recommendations, "🔄 Много циклов зарядки - следите за емкостью")
	}
	
	if len(recommendations) == 0 {
		recommendations = append(recommendations, "✅ Батарея работает нормально")
	}
	
	for _, rec := range recommendations {
		content.WriteString("• " + rec + "\n")
	}
	
	return content.String()
}

// renderDashboardControls рендерит панель управления дашборда
func (a *App) renderDashboardControls() string {
	controls := []string{
		"q - Выход в меню",
		"r - Обновить",
		"↑↓ - Скролл",
		"h - Справка",
	}
	
	controlsText := strings.Join(controls, " • ")
	
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Render(controlsText)
}

// getBatteryColor возвращает цвет для индикации заряда батареи
func getBatteryColor(percentage int) lipgloss.Color {
	switch {
	case percentage >= 80:
		return lipgloss.Color("green")
	case percentage >= 50:
		return lipgloss.Color("yellow")
	case percentage >= 20:
		return lipgloss.Color("orange")
	default:
		return lipgloss.Color("red")
	}
}