package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
)

// getVersion возвращает версию приложения
func getVersion() string {
	return "2.0"
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
	color.New(color.FgBlue).Println("⌨️  Горячие клавиши:")
	fmt.Println("q/Q - Выход из приложения")
	fmt.Println("h/H/? - Показать справку")
	fmt.Println("v/V - Показать версию")
	fmt.Println("d/D - Дашборд (главный экран)")
	fmt.Println("r/R - Отчеты и аналитика")
	fmt.Println("e/E - Экспорт данных")
	fmt.Println("s/S - Настройки")
	fmt.Println("1-7 - Быстрая диагностика")
	fmt.Println("↑↓ - Навигация по меню")
	fmt.Println("Enter - Выбор пункта меню")
	fmt.Println("Esc - Назад/отмена")
	fmt.Println()
	color.New(color.FgMagenta).Println("🚀 Командная строка:")
	fmt.Println("batmon -export-md <файл>  - Экспорт в Markdown")
	fmt.Println("batmon -export-html <файл> - Экспорт в HTML")
	fmt.Println("batmon -version           - Показать версию")
	fmt.Println("batmon -help              - Показать эту справку")
	fmt.Println()
	color.New(color.FgRed).Println("⚠️  Примечания:")
	fmt.Println("• Приложение требует macOS для работы с батареей")
	fmt.Println("• Данные сохраняются в ~/.local/share/batmon/")
	fmt.Println("• Для точных показаний используется pmset и ioreg")
	fmt.Println("• Температура и расширенные метрики доступны не на всех Mac")
	fmt.Println()
}

// runExportMode запускает режим экспорта
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
		fullPath, err := getExportPath(markdownFile)
		if err != nil {
			return fmt.Errorf("определение пути MD файла: %w", err)
		}
		
		if err := exportToMarkdown(data, fullPath); err != nil {
			return fmt.Errorf("экспорт в Markdown: %w", err)
		}
		exported = append(exported, fullPath)
		if !quiet {
			fmt.Printf("✅ Экспорт в Markdown: %s\n", fullPath)
		}
	}
	
	// Экспорт в HTML
	if htmlFile != "" {
		fullPath, err := getExportPath(htmlFile)
		if err != nil {
			return fmt.Errorf("определение пути HTML файла: %w", err)
		}
		
		if err := exportToHTML(data, fullPath); err != nil {
			return fmt.Errorf("экспорт в HTML: %w", err)
		}
		exported = append(exported, fullPath)
		if !quiet {
			fmt.Printf("✅ Экспорт в HTML: %s\n", fullPath)
		}
	}
	
	if !quiet && len(exported) > 0 {
		fmt.Printf("🎉 Успешно экспортировано файлов: %d\n", len(exported))
	}
	
	return nil
}

// generateReportData генерирует данные для отчета
func generateReportData(db DatabaseInterface) (ReportData, error) {
	var data ReportData
	
	// Получаем последние измерения
	measurements, err := getLastNMeasurements(db, 100)
	if err != nil {
		return data, fmt.Errorf("получение измерений: %w", err)
	}
	
	if len(measurements) == 0 {
		return data, fmt.Errorf("нет данных для экспорта")
	}
	
	data.Measurements = measurements
	data.GeneratedAt = time.Now()
	data.Version = getVersion()
	
	// Анализ здоровья батареи
	healthAnalysis := analyzeBatteryHealth(measurements)
	if healthAnalysis != nil {
		if wear, ok := healthAnalysis["wear_percentage"].(float64); ok {
			data.WearLevel = wear
		}
		if status, ok := healthAnalysis["health_status"].(string); ok {
			data.HealthStatus = status
		}
		if recommendations, ok := healthAnalysis["recommendations"].([]string); ok {
			data.Recommendations = recommendations
		}
		if cycles, ok := healthAnalysis["charge_cycles"].([]ChargeCycle); ok {
			data.ChargeCycles = cycles
		}
		if anomalies, ok := healthAnalysis["anomalies"].([]string); ok {
			data.Anomalies = anomalies
		}
	}
	
	// Анализ трендов
	data.TrendAnalysis = analyzeCapacityTrend(measurements)
	
	// Расширенные метрики
	data.AdvancedMetrics = analyzeAdvancedMetrics(measurements)
	
	return data, nil
}

// exportToMarkdown экспортирует данные в формат Markdown
func exportToMarkdown(data ReportData, filename string) error {
	var content strings.Builder
	
	content.WriteString("# 🔋 Отчет о состоянии батареи BatMon\n\n")
	content.WriteString(fmt.Sprintf("**Дата генерации:** %s\n", data.GeneratedAt.Format("2006-01-02 15:04:05")))
	content.WriteString(fmt.Sprintf("**Версия BatMon:** %s\n\n", data.Version))
	
	if len(data.Measurements) > 0 {
		latest := data.Measurements[len(data.Measurements)-1]
		content.WriteString("## 📊 Текущее состояние\n\n")
		content.WriteString(fmt.Sprintf("- **Заряд:** %d%%\n", latest.Percentage))
		content.WriteString(fmt.Sprintf("- **Состояние:** %s\n", formatStateWithEmoji(latest.State, latest.Percentage)))
		content.WriteString(fmt.Sprintf("- **Циклы заряда:** %d\n", latest.CycleCount))
		content.WriteString(fmt.Sprintf("- **Полная емкость:** %d мАч\n", latest.FullChargeCap))
		content.WriteString(fmt.Sprintf("- **Проектная емкость:** %d мАч\n", latest.DesignCapacity))
		content.WriteString(fmt.Sprintf("- **Износ:** %.1f%%\n", data.WearLevel))
		content.WriteString(fmt.Sprintf("- **Температура:** %d°C\n", latest.Temperature))
		content.WriteString(fmt.Sprintf("- **Напряжение:** %d мВ\n", latest.Voltage))
		if latest.AppleCondition != "" {
			content.WriteString(fmt.Sprintf("- **Состояние от Apple:** %s\n", latest.AppleCondition))
		}
		content.WriteString("\n")
	}
	
	// Здоровье батареи
	content.WriteString("## 💊 Здоровье батареи\n\n")
	content.WriteString(fmt.Sprintf("**Общая оценка:** %s\n\n", data.HealthStatus))
	
	// Расширенные метрики
	if data.AdvancedMetrics.HealthRating > 0 {
		content.WriteString("### 📈 Расширенные метрики\n\n")
		content.WriteString(fmt.Sprintf("- **Рейтинг здоровья:** %d/100\n", data.AdvancedMetrics.HealthRating))
		if data.AdvancedMetrics.VoltageStability > 0 {
			content.WriteString(fmt.Sprintf("- **Стабильность напряжения:** %.1f%%\n", data.AdvancedMetrics.VoltageStability))
		}
		if data.AdvancedMetrics.PowerEfficiency > 0 {
			content.WriteString(fmt.Sprintf("- **Эффективность энергопотребления:** %.1f\n", data.AdvancedMetrics.PowerEfficiency))
		}
		if data.AdvancedMetrics.PowerTrend != "" {
			content.WriteString(fmt.Sprintf("- **Тренд энергопотребления:** %s\n", data.AdvancedMetrics.PowerTrend))
		}
		content.WriteString("\n")
	}
	
	// Анализ трендов
	if !data.TrendAnalysis.IsHealthy {
		content.WriteString("### 📉 Анализ деградации\n\n")
		content.WriteString(fmt.Sprintf("- **Скорость деградации:** %.2f%% в месяц\n", data.TrendAnalysis.DegradationRate))
		if data.TrendAnalysis.ProjectedLifetime > 0 {
			content.WriteString(fmt.Sprintf("- **Прогноз до 80%% емкости:** %d дней\n", data.TrendAnalysis.ProjectedLifetime))
		}
		content.WriteString("\n")
	}
	
	// Рекомендации
	if len(data.Recommendations) > 0 {
		content.WriteString("## 💡 Рекомендации\n\n")
		for _, rec := range data.Recommendations {
			content.WriteString(fmt.Sprintf("- %s\n", rec))
		}
		content.WriteString("\n")
	}
	
	// Аномалии
	if len(data.Anomalies) > 0 {
		content.WriteString("## ⚠️ Обнаруженные аномалии\n\n")
		for _, anomaly := range data.Anomalies {
			content.WriteString(fmt.Sprintf("- %s\n", anomaly))
		}
		content.WriteString("\n")
	}
	
	// Циклы заряда-разряда
	if len(data.ChargeCycles) > 0 {
		content.WriteString("## 🔄 Последние циклы заряда\n\n")
		content.WriteString("| Тип | Начало | Конец | Изменение заряда |\n")
		content.WriteString("|-----|--------|-------|------------------|\n")
		
		// Показываем только последние 10 циклов
		start := 0
		if len(data.ChargeCycles) > 10 {
			start = len(data.ChargeCycles) - 10
		}
		
		for i := start; i < len(data.ChargeCycles); i++ {
			cycle := data.ChargeCycles[i]
			content.WriteString(fmt.Sprintf("| %s | %s | %s | %d%% → %d%% |\n",
				cycle.CycleType,
				cycle.StartTime.Format("15:04:05"),
				cycle.EndTime.Format("15:04:05"),
				cycle.StartPercent,
				cycle.EndPercent))
		}
		content.WriteString("\n")
	}
	
	// Статистика данных
	content.WriteString("## 📊 Статистика данных\n\n")
	content.WriteString(fmt.Sprintf("- **Всего измерений:** %d\n", len(data.Measurements)))
	if len(data.Measurements) > 0 {
		first := data.Measurements[0]
		latest := data.Measurements[len(data.Measurements)-1]
		firstTime, _ := time.Parse(time.RFC3339, first.Timestamp)
		latestTime, _ := time.Parse(time.RFC3339, latest.Timestamp)
		content.WriteString(fmt.Sprintf("- **Период наблюдений:** %s - %s\n", 
			firstTime.Format("2006-01-02 15:04"), 
			latestTime.Format("2006-01-02 15:04")))
		content.WriteString(fmt.Sprintf("- **Длительность:** %s\n", 
			formatDuration(latestTime.Sub(firstTime))))
	}
	
	content.WriteString("\n---\n")
	content.WriteString("*Отчет сгенерирован автоматически BatMon*")
	
	return os.WriteFile(filename, []byte(content.String()), 0644)
}

// exportToHTML экспортирует данные в формат HTML
func exportToHTML(data ReportData, filename string) error {
	var content strings.Builder
	
	content.WriteString("<!DOCTYPE html>\n<html lang=\"ru\">\n<head>\n")
	content.WriteString("<meta charset=\"UTF-8\">\n")
	content.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n")
	content.WriteString("<title>🔋 Отчет BatMon</title>\n")
	content.WriteString("<style>\n")
	content.WriteString(`
		body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 40px; background: #f5f5f7; color: #333; }
		.container { max-width: 800px; margin: 0 auto; background: white; padding: 40px; border-radius: 12px; box-shadow: 0 4px 20px rgba(0,0,0,0.1); }
		h1 { color: #1d1d1f; border-bottom: 3px solid #007AFF; padding-bottom: 10px; }
		h2 { color: #007AFF; margin-top: 30px; }
		h3 { color: #34C759; }
		.status-good { color: #34C759; font-weight: bold; }
		.status-warning { color: #FF9500; font-weight: bold; }
		.status-critical { color: #FF3B30; font-weight: bold; }
		.metric { display: flex; justify-content: space-between; padding: 8px 0; border-bottom: 1px solid #f0f0f0; }
		.metric-name { font-weight: 600; }
		.metric-value { color: #007AFF; }
		.recommendation { background: #E3F2FD; padding: 12px; margin: 8px 0; border-radius: 8px; border-left: 4px solid #2196F3; }
		.anomaly { background: #FFEBEE; padding: 12px; margin: 8px 0; border-radius: 8px; border-left: 4px solid #F44336; }
		table { width: 100%; border-collapse: collapse; margin: 20px 0; }
		th, td { padding: 12px; text-align: left; border-bottom: 1px solid #e0e0e0; }
		th { background: #f8f9fa; font-weight: 600; }
		.footer { margin-top: 40px; text-align: center; color: #666; font-size: 14px; }
	`)
	content.WriteString("</style>\n</head>\n<body>\n")
	content.WriteString("<div class=\"container\">\n")
	
	content.WriteString("<h1>🔋 Отчет о состоянии батареи BatMon</h1>\n")
	content.WriteString(fmt.Sprintf("<p><strong>Дата генерации:</strong> %s</p>\n", data.GeneratedAt.Format("2006-01-02 15:04:05")))
	content.WriteString(fmt.Sprintf("<p><strong>Версия BatMon:</strong> %s</p>\n", data.Version))
	
	if len(data.Measurements) > 0 {
		latest := data.Measurements[len(data.Measurements)-1]
		content.WriteString("<h2>📊 Текущее состояние</h2>\n")
		content.WriteString("<div class=\"metrics\">\n")
		
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Заряд:</div><div class=\"metric-value\">%d%%</div></div>\n", latest.Percentage))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Состояние:</div><div class=\"metric-value\">%s</div></div>\n", formatStateWithEmoji(latest.State, latest.Percentage)))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Циклы заряда:</div><div class=\"metric-value\">%d</div></div>\n", latest.CycleCount))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Полная емкость:</div><div class=\"metric-value\">%d мАч</div></div>\n", latest.FullChargeCap))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Проектная емкость:</div><div class=\"metric-value\">%d мАч</div></div>\n", latest.DesignCapacity))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Износ:</div><div class=\"metric-value\">%.1f%%</div></div>\n", data.WearLevel))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Температура:</div><div class=\"metric-value\">%d°C</div></div>\n", latest.Temperature))
		content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Напряжение:</div><div class=\"metric-value\">%d мВ</div></div>\n", latest.Voltage))
		if latest.AppleCondition != "" {
			content.WriteString(fmt.Sprintf("<div class=\"metric\"><div class=\"metric-name\">Состояние от Apple:</div><div class=\"metric-value\">%s</div></div>\n", latest.AppleCondition))
		}
		
		content.WriteString("</div>\n")
	}
	
	// Здоровье батареи
	content.WriteString("<h2>💊 Здоровье батареи</h2>\n")
	statusClass := "status-good"
	if strings.Contains(strings.ToLower(data.HealthStatus), "плохое") || strings.Contains(strings.ToLower(data.HealthStatus), "требует") {
		statusClass = "status-critical"
	} else if strings.Contains(strings.ToLower(data.HealthStatus), "удовлетворительное") || strings.Contains(strings.ToLower(data.HealthStatus), "внимания") {
		statusClass = "status-warning"
	}
	content.WriteString(fmt.Sprintf("<p class=\"%s\">Общая оценка: %s</p>\n", statusClass, data.HealthStatus))
	
	// Рекомендации
	if len(data.Recommendations) > 0 {
		content.WriteString("<h2>💡 Рекомендации</h2>\n")
		for _, rec := range data.Recommendations {
			content.WriteString(fmt.Sprintf("<div class=\"recommendation\">%s</div>\n", rec))
		}
	}
	
	// Аномалии
	if len(data.Anomalies) > 0 {
		content.WriteString("<h2>⚠️ Обнаруженные аномалии</h2>\n")
		for _, anomaly := range data.Anomalies {
			content.WriteString(fmt.Sprintf("<div class=\"anomaly\">%s</div>\n", anomaly))
		}
	}
	
	// Статистика данных
	content.WriteString("<h2>📊 Статистика данных</h2>\n")
	content.WriteString(fmt.Sprintf("<p><strong>Всего измерений:</strong> %d</p>\n", len(data.Measurements)))
	if len(data.Measurements) > 0 {
		first := data.Measurements[0]
		latest := data.Measurements[len(data.Measurements)-1]
		firstTime, _ := time.Parse(time.RFC3339, first.Timestamp)
		latestTime, _ := time.Parse(time.RFC3339, latest.Timestamp)
		content.WriteString(fmt.Sprintf("<p><strong>Период наблюдений:</strong> %s - %s</p>\n", 
			firstTime.Format("2006-01-02 15:04"), 
			latestTime.Format("2006-01-02 15:04")))
		content.WriteString(fmt.Sprintf("<p><strong>Длительность:</strong> %s</p>\n", 
			formatDuration(latestTime.Sub(firstTime))))
	}
	
	content.WriteString("<div class=\"footer\">")
	content.WriteString("<p><em>Отчет сгенерирован автоматически BatMon</em></p>")
	content.WriteString("</div>")
	content.WriteString("</div>\n</body>\n</html>")
	
	return os.WriteFile(filename, []byte(content.String()), 0644)
}