package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// analyzeAdvancedMetrics проводит анализ расширенных метрик батареи
func analyzeAdvancedMetrics(measurements []Measurement) AdvancedMetrics {
	if len(measurements) == 0 {
		return AdvancedMetrics{}
	}

	var metrics AdvancedMetrics
	latest := measurements[len(measurements)-1]

	// Анализируем стабильность напряжения
	voltages := make([]float64, 0)
	powers := make([]float64, 0)
	chargingEfficiencies := make([]float64, 0)

	for _, m := range measurements {
		if m.Voltage > 0 {
			voltages = append(voltages, float64(m.Voltage))
		}
		if m.Power != 0 {
			powers = append(powers, float64(m.Power))
		}

		// Эффективность зарядки (емкость / мощность)
		if m.Power > 0 && m.CurrentCapacity > 0 {
			efficiency := float64(m.CurrentCapacity) / float64(m.Power)
			chargingEfficiencies = append(chargingEfficiencies, efficiency)
		}
	}

	// Стабильность напряжения (коэффициент вариации)
	if len(voltages) > 1 {
		mean := 0.0
		for _, v := range voltages {
			mean += v
		}
		mean /= float64(len(voltages))

		variance := 0.0
		for _, v := range voltages {
			variance += (v - mean) * (v - mean)
		}
		variance /= float64(len(voltages))
		stdDev := math.Sqrt(variance)

		if mean > 0 {
			metrics.VoltageStability = 100 * (1 - stdDev/mean) // В процентах
		}
	}

	// Эффективность энергопотребления
	if len(powers) > 0 {
		avgPower := 0.0
		for _, p := range powers {
			avgPower += math.Abs(p) // Берем абсолютную величину
		}
		avgPower /= float64(len(powers))

		// Нормализуем эффективность (меньше мощность = выше эффективность)
		if avgPower > 0 {
			metrics.PowerEfficiency = math.Max(0, 100-avgPower/100)
		}
	}

	// Эффективность зарядки
	if len(chargingEfficiencies) > 0 {
		avgEfficiency := 0.0
		for _, e := range chargingEfficiencies {
			avgEfficiency += e
		}
		metrics.ChargingEfficiency = avgEfficiency / float64(len(chargingEfficiencies))
	}

	// Тренд энергопотребления
	if len(powers) >= 3 {
		recent := powers[len(powers)-3:]
		trend := "стабильное"

		if len(recent) == 3 {
			if recent[2] > recent[1] && recent[1] > recent[0] {
				trend = "растущее потребление"
			} else if recent[2] < recent[1] && recent[1] < recent[0] {
				trend = "снижающееся потребление"
			}
		}
		metrics.PowerTrend = trend
	}

	// Общий рейтинг здоровья
	healthScore := 100

	// Снижаем за износ
	if latest.DesignCapacity > 0 {
		wear := float64(latest.DesignCapacity-latest.FullChargeCap) / float64(latest.DesignCapacity) * 100
		healthScore -= int(wear * 0.5) // Износ влияет на 50%
	}

	// Снижаем за циклы
	cycleImpact := latest.CycleCount / 10 // Каждые 10 циклов = -1 балл
	healthScore -= cycleImpact

	// Снижаем за температуру
	if latest.Temperature > 45 {
		healthScore -= (latest.Temperature - 45) // Каждый градус свыше 45°C = -1 балл
	}

	// Учитываем стабильность напряжения
	if metrics.VoltageStability < 95 {
		healthScore -= int(95 - metrics.VoltageStability)
	}

	metrics.HealthRating = int(math.Max(0, float64(healthScore)))

	// Статус от Apple
	metrics.AppleStatus = latest.AppleCondition
	if metrics.AppleStatus == "" {
		if metrics.HealthRating >= 85 {
			metrics.AppleStatus = "Normal"
		} else if metrics.HealthRating >= 70 {
			metrics.AppleStatus = "Service Recommended"
		} else {
			metrics.AppleStatus = "Replace Soon"
		}
	}

	return metrics
}

// detectBatteryAnomalies анализирует аномальные изменения заряда с нормализованными порогами
func detectBatteryAnomalies(ms []Measurement) []string {
	if len(ms) < 2 {
		return nil
	}

	var anomalies []string

	for i := 0; i < len(ms)-1; i++ {
		prev := ms[i]
		curr := ms[i+1]

		// Вычисляем интервал времени между измерениями
		prevTime, err1 := time.Parse(time.RFC3339, prev.Timestamp)
		currTime, err2 := time.Parse(time.RFC3339, curr.Timestamp)
		var interval time.Duration = 30 * time.Second // по умолчанию
		if err1 == nil && err2 == nil {
			interval = currTime.Sub(prevTime)
		}

		// Получаем нормализованные пороги
		chargeThreshold, capacityThreshold := normalizeAnomalyThresholds(interval)

		// Резкий скачок заряда
		chargeDiff := curr.Percentage - prev.Percentage
		if chargeDiff > chargeThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкий рост заряда: %d%% → %d%% за %.1f мин (%s)",
				prev.Percentage, curr.Percentage, interval.Minutes(), curr.Timestamp[11:19]))
		}

		// Резкое падение заряда
		if chargeDiff < -chargeThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкое падение заряда: %d%% → %d%% за %.1f мин (%s)",
				prev.Percentage, curr.Percentage, interval.Minutes(), curr.Timestamp[11:19]))
		}

		// Неожиданное изменение состояния
		if prev.State != curr.State {
			anomalies = append(anomalies, fmt.Sprintf("Смена состояния: %s → %s (%s)",
				prev.State, curr.State, curr.Timestamp[11:19]))
		}

		// Резкое изменение емкости
		capacityDiff := abs(curr.CurrentCapacity - prev.CurrentCapacity)
		if capacityDiff > capacityThreshold {
			anomalies = append(anomalies, fmt.Sprintf("Резкое изменение емкости: %d → %d мАч за %.1f мин (%s)",
				prev.CurrentCapacity, curr.CurrentCapacity, interval.Minutes(), curr.Timestamp[11:19]))
		}
	}

	return anomalies
}

// analyzeCapacityTrend анализирует тренд деградации батареи
func analyzeCapacityTrend(measurements []Measurement) TrendAnalysis {
	if len(measurements) < 10 {
		return TrendAnalysis{IsHealthy: true} // Недостаточно данных для анализа
	}

	// Ищем измерения за последние 30 дней с system_profiler данными
	now := time.Now()
	thirtyDaysAgo := now.AddDate(0, 0, -30)

	var validMeasurements []Measurement
	for _, m := range measurements {
		if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
			if t.After(thirtyDaysAgo) && m.FullChargeCap > 0 && m.DesignCapacity > 0 {
				validMeasurements = append(validMeasurements, m)
			}
		}
	}

	if len(validMeasurements) < 5 {
		return TrendAnalysis{IsHealthy: true} // Недостаточно данных
	}

	// Простая линейная регрессия для тренда емкости
	first := validMeasurements[0]
	last := validMeasurements[len(validMeasurements)-1]

	firstTime, _ := time.Parse(time.RFC3339, first.Timestamp)
	lastTime, _ := time.Parse(time.RFC3339, last.Timestamp)

	daysDiff := lastTime.Sub(firstTime).Hours() / 24
	if daysDiff < 7 { // Менее недели данных
		return TrendAnalysis{IsHealthy: true}
	}

	capacityDiff := float64(last.FullChargeCap - first.FullChargeCap)
	dailyDegradation := capacityDiff / daysDiff
	monthlyDegradation := dailyDegradation * 30

	// Рассчитываем процент деградации от проектной емкости
	monthlyDegradationPercent := (monthlyDegradation / float64(last.DesignCapacity)) * 100

	// Прогноз времени до 80% емкости
	currentHealthPercent := (float64(last.FullChargeCap) / float64(last.DesignCapacity)) * 100
	targetHealthPercent := 80.0

	var projectedDays int
	if monthlyDegradationPercent < 0 && currentHealthPercent > targetHealthPercent {
		monthsTo80Percent := (currentHealthPercent - targetHealthPercent) / (-monthlyDegradationPercent)
		projectedDays = int(monthsTo80Percent * 30)
	}

	// Считаем здоровой деградацию менее 0.5% в месяц
	isHealthy := monthlyDegradationPercent > -0.5

	return TrendAnalysis{
		DegradationRate:   monthlyDegradationPercent,
		ProjectedLifetime: projectedDays,
		IsHealthy:         isHealthy,
	}
}

// detectChargeCycles обнаруживает циклы заряда-разряда
func detectChargeCycles(measurements []Measurement) []ChargeCycle {
	if len(measurements) < 3 {
		return nil
	}

	var cycles []ChargeCycle
	var currentCycle *ChargeCycle

	for i, m := range measurements {
		timestamp, err := time.Parse(time.RFC3339, m.Timestamp)
		if err != nil {
			continue
		}

		if i == 0 {
			continue // Пропускаем первое измерение
		}

		prev := measurements[i-1]

		// Определяем смену направления (заряд/разряд)
		if prev.State != m.State {
			if currentCycle != nil {
				// Завершаем текущий цикл
				currentCycle.EndTime = timestamp
				currentCycle.EndPercent = m.Percentage

				if prev.CurrentCapacity > 0 && m.CurrentCapacity > 0 {
					currentCycle.CapacityLoss = prev.CurrentCapacity - m.CurrentCapacity
				}

				cycles = append(cycles, *currentCycle)
			}

			// Начинаем новый цикл
			currentCycle = &ChargeCycle{
				StartTime:    timestamp,
				StartPercent: m.Percentage,
				CycleType:    strings.ToLower(m.State),
			}
		}

		// Обновляем текущий цикл
		if currentCycle != nil {
			currentCycle.EndTime = timestamp
			currentCycle.EndPercent = m.Percentage
		}
	}

	// Завершаем последний цикл если есть
	if currentCycle != nil {
		cycles = append(cycles, *currentCycle)
	}

	return cycles
}

// analyzeBatteryHealth анализирует общее состояние батареи
func analyzeBatteryHealth(ms []Measurement) map[string]interface{} {
	if len(ms) == 0 {
		return nil
	}

	latest := ms[len(ms)-1]
	analysis := make(map[string]interface{})

	// Основные метрики
	wear := computeWear(latest.DesignCapacity, latest.FullChargeCap)
	analysis["wear_percentage"] = wear
	analysis["cycle_count"] = latest.CycleCount

	// Анализ аномалий
	anomalies := detectBatteryAnomalies(ms)
	analysis["anomalies"] = anomalies
	analysis["anomaly_count"] = len(anomalies)

	// Робастная скорость разрядки
	avgRate, validIntervals := computeAvgRateRobust(ms, 10)
	analysis["discharge_rate"] = avgRate
	analysis["valid_intervals"] = validIntervals

	// Анализ трендов
	trendAnalysis := analyzeCapacityTrend(ms)
	analysis["trend_analysis"] = trendAnalysis

	// Анализ циклов заряда-разряда
	chargeCycles := detectChargeCycles(ms)
	analysis["charge_cycles"] = chargeCycles

	// Оценка здоровья батареи
	var healthStatus string
	var healthScore int

	switch {
	case wear < 5 && latest.CycleCount < 300:
		healthStatus = "Отличное"
		healthScore = 95
	case wear < 10 && latest.CycleCount < 500:
		healthStatus = "Хорошее"
		healthScore = 85
	case wear < 20 && latest.CycleCount < 800:
		healthStatus = "Удовлетворительное"
		healthScore = 70
	case wear < 30 && latest.CycleCount < 1200:
		healthStatus = "Требует внимания"
		healthScore = 50
	default:
		healthStatus = "Плохое"
		healthScore = 30
	}

	// Корректировка на основе аномалий
	if len(anomalies) > 5 {
		healthScore -= 10
		healthStatus += " (нестабильная работа)"
	}

	// Корректировка на основе тренда
	if !trendAnalysis.IsHealthy && trendAnalysis.DegradationRate < -1.0 {
		healthScore -= 15
		healthStatus += " (быстрая деградация)"
	}

	analysis["health_status"] = healthStatus
	analysis["health_score"] = healthScore

	// Расширенные рекомендации
	var recommendations []string

	// Рекомендации по замене
	if wear > 20 {
		recommendations = append(recommendations, "Рассмотрите замену батареи")
	}

	// Рекомендации по аномалиям
	if len(anomalies) > 3 {
		recommendations = append(recommendations, "Проверьте настройки энергосбережения")
	}

	// Рекомендации по циклам
	if latest.CycleCount > 1000 {
		recommendations = append(recommendations, "Батарея приближается к концу жизненного цикла")
	}

	// Рекомендации по энергопотреблению
	if avgRate > 1000 {
		recommendations = append(recommendations, "Высокое энергопотребление - закройте ресурсоемкие приложения")
	}

	// Рекомендации по температуре
	if latest.Temperature > 40 {
		recommendations = append(recommendations, "Высокая температура батареи ("+strconv.Itoa(latest.Temperature)+"°C) - избегайте нагрузки")
	} else if latest.Temperature > 35 {
		recommendations = append(recommendations, "Повышенная температура батареи - рассмотрите улучшение охлаждения")
	}

	// Рекомендации по трендам
	if !trendAnalysis.IsHealthy && trendAnalysis.DegradationRate < -0.5 {
		recommendations = append(recommendations, fmt.Sprintf("Быстрая деградация батареи (%.2f%% в месяц) - проверьте условия эксплуатации", -trendAnalysis.DegradationRate))
	}

	// Рекомендации по заряду
	if latest.State == "charging" && latest.Percentage == 100 {
		recommendations = append(recommendations, "Не держите батарею постоянно на 100% заряда")
	}

	// Рекомендации по калибровке
	if wear > 15 && latest.CycleCount > 500 {
		recommendations = append(recommendations, "Рассмотрите калибровку батареи (полный разряд и заряд)")
	}

	analysis["recommendations"] = recommendations

	return analysis
}