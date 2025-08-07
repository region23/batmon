package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// parsePMSet получает процент заряда и состояние питания из pmset.
func parsePMSet() (int, string, error) {
	cmd := exec.Command("pmset", "-g", "batt")
	out, err := cmd.Output()
	if err != nil {
		return 0, "", fmt.Errorf("pmset: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	re := regexp.MustCompile(`(\d+)%\s*;\s*(\w+)`)
	for scanner.Scan() {
		line := scanner.Text()
		m := re.FindStringSubmatch(line)
		if len(m) == 3 {
			pct, _ := strconv.Atoi(m[1])
			state := strings.ToLower(m[2])
			return pct, state, nil
		}
	}
	if err = scanner.Err(); err != nil {
		return 0, "", fmt.Errorf("сканирование pmset: %w", err)
	}
	return 0, "", fmt.Errorf("данные о батарее не найдены")
}

// parseSystemProfiler получает данные из system_profiler.
// На Apple Silicon многие параметры недоступны, используем то, что есть
func parseSystemProfiler() (cycle, fullCap, designCap, currCap, temperature, voltage, amperage int, condition string, err error) {
	cmd := exec.Command("system_profiler", "SPPowerDataType", "-detailLevel", "full")
	out, cmdErr := cmd.Output()
	if cmdErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("system_profiler: %w", cmdErr)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Cycle Count:"):
			val := strings.TrimSpace(strings.TrimPrefix(line, "Cycle Count:"))
			cycle, _ = strconv.Atoi(val)
		case strings.HasPrefix(line, "Condition:"):
			condition = strings.TrimSpace(strings.TrimPrefix(line, "Condition:"))
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("сканирование system_profiler: %w", scanErr)
	}
	return cycle, fullCap, designCap, currCap, temperature, voltage, amperage, condition, nil
}

// parseIORegistry получает подробные данные о батарее из ioreg
func parseIORegistry() (cycle, fullCap, designCap, currCap, temperature, voltage, amperage int, condition string, err error) {
	cmd := exec.Command("ioreg", "-rn", "AppleSmartBattery")
	out, cmdErr := cmd.Output()
	if cmdErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("ioreg: %w", cmdErr)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Парсим параметры в формате "ParameterName" = Value
		if strings.Contains(line, " = ") {
			parts := strings.SplitN(line, " = ", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.Trim(parts[0], `"`)
			value := strings.TrimSpace(parts[1])

			switch key {
			case "CycleCount":
				cycle, _ = strconv.Atoi(value)
			case "AppleRawMaxCapacity":
				fullCap, _ = strconv.Atoi(value)
			case "DesignCapacity":
				designCap, _ = strconv.Atoi(value)
			case "AppleRawCurrentCapacity":
				currCap, _ = strconv.Atoi(value)
			case "Temperature":
				// Температура в сотых долях градуса
				if temp, err := strconv.Atoi(value); err == nil {
					temperature = temp / 100
				}
			case "Voltage":
				voltage, _ = strconv.Atoi(value)
			case "Amperage":
				// Amperage может быть большим uint64, которое представляет отрицательное число
				if amp, err := strconv.ParseUint(value, 10, 64); err == nil {
					if amp > 9223372036854775807 { // больше максимального int64
						// Это отрицательное число, представленное как uint64
						amperage = int(int64(amp))
					} else {
						amperage = int(amp)
					}
				}
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("сканирование ioreg: %w", scanErr)
	}

	// Получаем состояние батареи из system_profiler
	spCycle, _, _, _, _, _, _, spCondition, spErr := parseSystemProfiler()
	if spErr == nil {
		condition = spCondition
		if cycle == 0 {
			cycle = spCycle
		}
	}

	return cycle, fullCap, designCap, currCap, temperature, voltage, amperage, condition, nil
}

// isOnBattery проверяет, работает ли система от батареи
func isOnBattery() (bool, string, int, error) {
	pct, state, err := parsePMSet()
	if err != nil {
		return false, "", 0, err
	}

	isOnBatt := strings.ToLower(state) == "discharging" ||
		strings.ToLower(state) == "finishing" ||
		strings.ToLower(state) == "charged"

	return isOnBatt, state, pct, nil
}