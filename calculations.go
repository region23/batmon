package main

import (
	"time"
)

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

// normalizeAnomalyThresholds нормализует пороги аномалий на время
func normalizeAnomalyThresholds(interval time.Duration) (int, int) {
	// Базовые пороги для 30-секундного интервала
	baseChargeThreshold := 20    // процентов
	baseCapacityThreshold := 500 // мАч

	// Нормализация на минуту
	minutes := interval.Minutes()
	if minutes < 0.5 {
		minutes = 0.5 // минимум 30 секунд
	}

	// Чем больше интервал, тем выше допустимые пороги
	normalizedChargeThreshold := int(float64(baseChargeThreshold) * minutes * 2) // 40% в минуту
	normalizedCapacityThreshold := int(float64(baseCapacityThreshold) * minutes)

	// Ограничиваем максимальные пороги
	if normalizedChargeThreshold > 50 {
		normalizedChargeThreshold = 50
	}
	if normalizedCapacityThreshold > 2000 {
		normalizedCapacityThreshold = 2000
	}

	return normalizedChargeThreshold, normalizedCapacityThreshold
}

// abs возвращает абсолютное значение
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// min возвращает минимальное значение
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max возвращает максимальное значение
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}