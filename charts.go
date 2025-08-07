package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Chart представляет ASCII график
type Chart struct {
	Title       string
	Data        []float64
	Width       int
	Height      int
	MinValue    float64
	MaxValue    float64
	Color       lipgloss.Color
	ShowAxes    bool
	FixedRange  bool // Флаг для фиксированного диапазона значений
}

// NewChart создает новый график
func NewChart(title string, width, height int) *Chart {
	return &Chart{
		Title:    title,
		Width:    width,
		Height:   height,
		Color:    lipgloss.Color("39"), // Синий цвет по умолчанию
		ShowAxes: true,
		Data:     make([]float64, 0),
	}
}

// SetData устанавливает данные для графика
func (c *Chart) SetData(data []float64) {
	c.Data = make([]float64, len(data))
	copy(c.Data, data)
	
	// Если используется фиксированный диапазон, не пересчитываем Min/Max
	if c.FixedRange {
		return
	}
	
	if len(data) > 0 {
		c.MinValue = data[0]
		c.MaxValue = data[0]
		
		for _, v := range data {
			if v < c.MinValue {
				c.MinValue = v
			}
			if v > c.MaxValue {
				c.MaxValue = v
			}
		}
		
		// Добавляем небольшой отступ для лучшей визуализации
		range_ := c.MaxValue - c.MinValue
		if range_ == 0 {
			range_ = 1
		}
		padding := range_ * 0.1
		c.MinValue -= padding
		c.MaxValue += padding
	}
}

// SetSize устанавливает новые размеры для графика
func (c *Chart) SetSize(width, height int) {
	if width > 0 {
		c.Width = width
	}
	if height > 0 {
		c.Height = height
	}
}

// Render рендерит график в строку
func (c *Chart) Render() string {
	if len(c.Data) == 0 {
		return c.renderEmpty()
	}
	
	var lines []string
	
	// Заголовок
	if c.Title != "" {
		titleStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(c.Color).
			Align(lipgloss.Center)
		lines = append(lines, titleStyle.Width(c.Width).Render(c.Title))
	}
	
	// График
	chart := c.renderChart()
	lines = append(lines, chart...)
	
	// Оси и подписи
	if c.ShowAxes {
		axes := c.renderAxes()
		lines = append(lines, axes...)
	}
	
	return strings.Join(lines, "\n")
}

// renderChart рендерит основную часть графика
func (c *Chart) renderChart() []string {
	chartHeight := c.Height
	if c.ShowAxes {
		chartHeight -= 2 // Место для осей
	}
	
	lines := make([]string, chartHeight)
	
	// Символы для рисования графика
	plotChars := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	
	// Вычисляем ширину доступную для данных
	dataWidth := c.Width
	if c.ShowAxes {
		dataWidth -= 6 // Место для Y-оси и отступов
	}
	
	// Подготавливаем данные для отображения
	chartData := c.prepareDataForWidth(dataWidth)
	
	// Рендерим каждую строку графика
	for row := 0; row < chartHeight; row++ {
		line := ""
		
		// Y-ось
		if c.ShowAxes {
			yValue := c.MaxValue - (float64(row)/float64(chartHeight-1))*(c.MaxValue-c.MinValue)
			yLabel := fmt.Sprintf("%4.0f│", yValue)
			line += lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(yLabel)
		}
		
		// Данные графика
		for col := 0; col < len(chartData); col++ {
			value := chartData[col]
			
			// Нормализуем значение от 0 до 1
			normalized := (value - c.MinValue) / (c.MaxValue - c.MinValue)
			if c.MaxValue == c.MinValue {
				normalized = 0.5
			}
			
			// Определяем высоту символа в текущей строке
			targetHeight := normalized * float64(chartHeight)
			currentRowFromBottom := float64(chartHeight - 1 - row)
			
			char := " "
			if targetHeight > currentRowFromBottom {
				// Определяем интенсивность символа
				intensity := math.Min(targetHeight-currentRowFromBottom, 1.0)
				charIndex := int(intensity * float64(len(plotChars)-1))
				if charIndex >= len(plotChars) {
					charIndex = len(plotChars) - 1
				}
				char = plotChars[charIndex]
			}
			
			// Применяем цвет
			styledChar := lipgloss.NewStyle().Foreground(c.Color).Render(char)
			line += styledChar
		}
		
		lines[row] = line
	}
	
	return lines
}

// prepareDataForWidth подготавливает данные под нужную ширину
func (c *Chart) prepareDataForWidth(targetWidth int) []float64 {
	if len(c.Data) == 0 {
		return make([]float64, targetWidth)
	}
	
	if len(c.Data) == targetWidth {
		return c.Data
	}
	
	result := make([]float64, targetWidth)
	
	if len(c.Data) < targetWidth {
		// Растягиваем данные
		for i := 0; i < targetWidth; i++ {
			sourceIndex := float64(i) * float64(len(c.Data)-1) / float64(targetWidth-1)
			leftIndex := int(sourceIndex)
			rightIndex := leftIndex + 1
			
			if rightIndex >= len(c.Data) {
				result[i] = c.Data[leftIndex]
			} else {
				// Линейная интерполяция
				t := sourceIndex - float64(leftIndex)
				result[i] = c.Data[leftIndex]*(1-t) + c.Data[rightIndex]*t
			}
		}
	} else {
		// Сжимаем данные (среднее значение)
		step := float64(len(c.Data)) / float64(targetWidth)
		for i := 0; i < targetWidth; i++ {
			start := int(float64(i) * step)
			end := int(float64(i+1) * step)
			if end > len(c.Data) {
				end = len(c.Data)
			}
			
			sum := 0.0
			count := 0
			for j := start; j < end; j++ {
				sum += c.Data[j]
				count++
			}
			
			if count > 0 {
				result[i] = sum / float64(count)
			}
		}
	}
	
	return result
}

// renderAxes рендерит оси координат
func (c *Chart) renderAxes() []string {
	lines := make([]string, 0)
	
	// X-ось
	xAxis := "    └" + strings.Repeat("─", c.Width-6)
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(xAxis))
	
	// Подписи к X-оси (опционально)
	if len(c.Data) > 1 {
		xLabels := fmt.Sprintf("     0%s%d", strings.Repeat(" ", c.Width-10), len(c.Data)-1)
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(xLabels))
	}
	
	return lines
}

// renderEmpty рендерит пустой график
func (c *Chart) renderEmpty() string {
	emptyMsg := "Нет данных для отображения"
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Align(lipgloss.Center).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Width(c.Width).
		Height(c.Height)
	
	return style.Render(emptyMsg)
}

// BatteryChart создает график заряда батареи
func NewBatteryChart(width, height int) *Chart {
	chart := NewChart("⚡ Заряд батареи (%)", width, height)
	chart.Color = lipgloss.Color("46") // Зеленый цвет
	// Фиксируем диапазон для процентов заряда от 0 до 100
	chart.MinValue = 0
	chart.MaxValue = 100
	chart.FixedRange = true
	return chart
}

// CapacityChart создает график емкости батареи
func NewCapacityChart(width, height int) *Chart {
	chart := NewChart("🔋 Емкость (мАч)", width, height)
	chart.Color = lipgloss.Color("39") // Синий цвет
	// Не фиксируем диапазон, чтобы он автоматически подстраивался под данные
	chart.FixedRange = false
	return chart
}

// TemperatureChart создает график температуры
func NewTemperatureChart(width, height int) *Chart {
	chart := NewChart("🌡️ Температура (°C)", width, height)
	chart.Color = lipgloss.Color("196") // Красный цвет
	return chart
}

// Sparkline создает мини-график (спарклайн)
type Sparkline struct {
	Data  []float64
	Width int
	Color lipgloss.Color
}

// NewSparkline создает новый спарклайн
func NewSparkline(width int) *Sparkline {
	return &Sparkline{
		Width: width,
		Color: lipgloss.Color("39"),
		Data:  make([]float64, 0),
	}
}

// SetData устанавливает данные для спарклайна
func (s *Sparkline) SetData(data []float64) {
	s.Data = make([]float64, len(data))
	copy(s.Data, data)
}

// SetWidth устанавливает новую ширину для спарклайна
func (s *Sparkline) SetWidth(width int) {
	if width > 0 {
		s.Width = width
	}
}

// Render рендерит спарклайн
func (s *Sparkline) Render() string {
	if len(s.Data) == 0 {
		return strings.Repeat("─", s.Width)
	}
	
	// Символы для спарклайна
	sparkChars := []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	
	// Подготавливаем данные под нужную ширину
	data := s.prepareDataForWidth(s.Width)
	
	// Находим мин и макс
	minVal, maxVal := data[0], data[0]
	for _, v := range data {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	
	result := ""
	for _, value := range data {
		// Нормализуем значение
		var normalized float64
		if maxVal == minVal {
			normalized = 0.5
		} else {
			normalized = (value - minVal) / (maxVal - minVal)
		}
		
		// Выбираем символ
		charIndex := int(normalized * float64(len(sparkChars)-1))
		if charIndex >= len(sparkChars) {
			charIndex = len(sparkChars) - 1
		}
		if charIndex < 0 {
			charIndex = 0
		}
		
		char := sparkChars[charIndex]
		result += lipgloss.NewStyle().Foreground(s.Color).Render(char)
	}
	
	return result
}

// prepareDataForWidth для спарклайна
func (s *Sparkline) prepareDataForWidth(targetWidth int) []float64 {
	if len(s.Data) == 0 {
		return make([]float64, targetWidth)
	}
	
	if len(s.Data) == targetWidth {
		return s.Data
	}
	
	result := make([]float64, targetWidth)
	
	if len(s.Data) < targetWidth {
		// Растягиваем данные
		for i := 0; i < targetWidth; i++ {
			sourceIndex := float64(i) * float64(len(s.Data)-1) / float64(targetWidth-1)
			leftIndex := int(sourceIndex)
			rightIndex := leftIndex + 1
			
			if rightIndex >= len(s.Data) {
				result[i] = s.Data[leftIndex]
			} else {
				t := sourceIndex - float64(leftIndex)
				result[i] = s.Data[leftIndex]*(1-t) + s.Data[rightIndex]*t
			}
		}
	} else {
		// Сжимаем данные
		step := float64(len(s.Data)) / float64(targetWidth)
		for i := 0; i < targetWidth; i++ {
			start := int(float64(i) * step)
			end := int(float64(i+1) * step)
			if end > len(s.Data) {
				end = len(s.Data)
			}
			
			sum := 0.0
			count := 0
			for j := start; j < end; j++ {
				sum += s.Data[j]
				count++
			}
			
			if count > 0 {
				result[i] = sum / float64(count)
			}
		}
	}
	
	return result
}