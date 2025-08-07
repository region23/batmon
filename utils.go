package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// getDataDir возвращает кроссплатформенную папку для данных приложения по стандарту XDG
func getDataDir() (string, error) {
	var dataDir string
	
	// Определяем папку в зависимости от ОС следуя XDG Base Directory Specification
	switch runtime.GOOS {
	case "windows":
		// Windows: %LOCALAPPDATA%\batmon (или %APPDATA%\batmon)
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			dataDir = filepath.Join(localAppData, "batmon")
		} else if appData := os.Getenv("APPDATA"); appData != "" {
			dataDir = filepath.Join(appData, "batmon")
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
			}
			dataDir = filepath.Join(homeDir, "AppData", "Local", "batmon")
		}
		
	case "darwin":
		// macOS: ~/.local/share/batmon (XDG-совместимо, как на Linux)
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
		}
		// Используем XDG_DATA_HOME или ~/.local/share
		if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
			dataDir = filepath.Join(xdgDataHome, "batmon")
		} else {
			dataDir = filepath.Join(homeDir, ".local", "share", "batmon")
		}
		
	default:
		// Linux и другие Unix: ~/.local/share/batmon (XDG Base Directory)
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
		}
		
		// Используем XDG_DATA_HOME если установлена, иначе ~/.local/share
		if xdgDataHome := os.Getenv("XDG_DATA_HOME"); xdgDataHome != "" {
			dataDir = filepath.Join(xdgDataHome, "batmon")
		} else {
			dataDir = filepath.Join(homeDir, ".local", "share", "batmon")
		}
	}
	
	// Создаем папку если её нет
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("не удалось создать папку для данных: %w", err)
	}
	
	return dataDir, nil
}

// getDBPath возвращает путь к файлу базы данных
func getDBPath() string {
	dataDir, err := getDataDir()
	if err != nil {
		// Fallback на текущую директорию если не можем создать папку данных
		log.Printf("Не удалось создать папку данных, используем текущую папку: %v", err)
		return "batmon.sqlite"
	}
	
	return filepath.Join(dataDir, "batmon.sqlite")
}

// getDocumentsDir возвращает путь к папке Documents пользователя
func getDocumentsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("не удалось получить домашнюю папку: %w", err)
	}
	
	documentsDir := filepath.Join(homeDir, "Documents")
	return documentsDir, nil
}

// getExportPath возвращает полный путь для экспортируемого файла
func getExportPath(filename string) (string, error) {
	// Если путь уже абсолютный, используем как есть
	if filepath.IsAbs(filename) {
		return filename, nil
	}
	
	// Если содержит разделители пути, используем как есть (относительный путь)
	if strings.Contains(filename, string(filepath.Separator)) {
		return filename, nil
	}
	
	// Иначе сохраняем в Documents
	documentsDir, err := getDocumentsDir()
	if err != nil {
		// Fallback на текущую директорию
		return filename, nil
	}
	
	return filepath.Join(documentsDir, filename), nil
}

// formatStateWithEmoji добавляет эмодзи к состоянию батареи
func formatStateWithEmoji(state string, percentage int) string {
	if state == "" {
		return "Неизвестно"
	}

	stateLower := strings.ToLower(state)
	stateFormatted := strings.ToUpper(string(stateLower[0])) + stateLower[1:]

	switch stateLower {
	case "charging":
		if percentage >= 90 {
			return "🔋 " + stateFormatted + " (почти полная)"
		}
		return "⚡ " + stateFormatted
	case "discharging":
		if percentage < 20 {
			return "🪫 " + stateFormatted + " (низкий заряд)"
		} else if percentage < 50 {
			return "🔋 " + stateFormatted
		}
		return "🔋 " + stateFormatted
	case "charged":
		return "✅ " + stateFormatted
	case "finishing":
		return "🔌 " + stateFormatted
	default:
		return stateFormatted
	}
}

// formatDuration форматирует время в читаемый вид
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	
	if hours > 0 {
		return fmt.Sprintf("%d ч %d мин", hours, minutes)
	}
	return fmt.Sprintf("%d мин", minutes)
}

// normalizeKeyInput нормализует ввод клавиш для поддержки разных раскладок клавиатуры
func normalizeKeyInput(keyID string) string {
	// Карта соответствий клавиш в разных раскладках к стандартным английским
	keyMappings := map[string]string{
		// Русская раскладка (ЙЦУКЕН)
		"й": "q", // q -> й
		"ц": "w", // w -> ц
		"у": "e", // e -> у
		"к": "r", // r -> к
		"е": "t", // t -> е
		"н": "y", // y -> н
		"г": "u", // u -> г
		"ш": "i", // i -> ш
		"щ": "o", // o -> щ
		"з": "p", // p -> з
		"ф": "a", // a -> ф
		"ы": "s", // s -> ы
		"в": "d", // d -> в
		"а": "f", // f -> а
		"п": "g", // g -> п
		"р": "h", // h -> р
		"о": "j", // j -> о
		"л": "k", // k -> л
		"д": "l", // l -> д
		"я": "z", // z -> я
		"ч": "x", // x -> ч
		"с": "c", // c -> с
		"м": "v", // v -> м
		"и": "b", // b -> и
		"т": "n", // n -> т
		"ь": "m", // m -> ь

		// Немецкая раскладка (QWERTZ) - только проблемные клавиши
		"ü": "y", // В немецкой y на месте ü
		"ä": "a", // и т.д.
	}

	// Проверяем, есть ли маппинг для данной клавиши
	if normalized, exists := keyMappings[keyID]; exists {
		return normalized
	}

	// Если маппинга нет, возвращаем исходную клавишу
	return keyID
}