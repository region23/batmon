# 🚀 Инструкции по созданию релизов BatMon

## 📋 Автоматическая сборка через GitHub Actions

### 🏷️ Создание релиза

1. **Создайте тег версии**:
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

2. **GitHub Actions автоматически**:
   - Соберёт Universal Binary (Intel + Apple Silicon)
   - Создаст macOS приложение (.app) с иконкой
   - Упакует в BatMon.zip для скачивания
   - Создаст GitHub Release с описанием
   - Добавит контрольную сумму SHA256

### 📦 Что включает релиз

- **BatMon.zip** - Готовое macOS приложение
- **BatMon.zip.sha256** - Контрольная сумма для проверки
- **Автоматические release notes** с инструкциями

### 🔧 Локальная сборка (для тестирования)

```bash
# Установить appify
go install github.com/machinebox/appify@latest

# Собрать бинарники
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o batmon-arm64
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o batmon-amd64

# Создать universal binary
lipo -create -output batmon-universal batmon-arm64 batmon-amd64

# Создать .app bundle
appify -name "BatMon" -icon "./logo.png" -id "com.batmon.app" -version "1.0" ./batmon-universal

# Упаковать для распространения
zip -r BatMon.zip BatMon.app
```

### 🏷️ Схема версионирования

- **v1.0.0** - Major релиз с новой функциональностью
- **v1.0.1** - Patch релиз с исправлениями
- **v1.1.0** - Minor релиз с небольшими улучшениями

### 🔍 Проверка релиза

После создания релиза проверьте:

1. **Скачивание**: BatMon.zip скачивается корректно
2. **Распаковка**: Архив распаковывается без ошибок  
3. **Запуск**: BatMon.app запускается на Mac
4. **Иконка**: Отображается правильная иконка приложения
5. **Функционал**: Все основные функции работают

### 📊 Совместимость

Создаваемые релизы поддерживают:

- ✅ **Apple Silicon**: M1, M2, M3 MacBook
- ✅ **Intel**: x86_64 MacBook  
- ✅ **macOS версии**: 12.0+ (Monterey и новее)
- ✅ **Universal Binary**: Один файл для всех Mac

### 🛠️ Troubleshooting

**Если сборка падает**:
- Проверьте что logo.png существует в корне проекта
- Убедитесь что Go код компилируется локально
- Проверьте права доступа к GitHub Actions

**Если macOS блокирует приложение**:
- Это нормально для неподписанных приложений
- Пользователи могут разрешить в System Preferences
- Для production нужен Apple Developer Account для подписи