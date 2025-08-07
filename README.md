# 🔋 BatMon - Мониторинг и умный анализ состояния батареи MacBook

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/region23/batmon)](https://github.com/region23/batmon/releases/latest)
[![GitHub downloads](https://img.shields.io/github/downloads/region23/batmon/total)](https://github.com/region23/batmon/releases)
[![GitHub stars](https://img.shields.io/github/stars/region23/batmon)](https://github.com/region23/batmon/stargazers)
[![macOS](https://img.shields.io/badge/macOS-12.0+-blue)](https://github.com/region23/batmon/releases)
[![Apple Silicon](https://img.shields.io/badge/Apple%20Silicon-M1%2FM2%2FM3-green)](https://github.com/region23/batmon/releases)

*Продвинутое macOS приложение для мониторинга и анализа состояния батареи MacBook с интерактивным дашбордом, детальной аналитикой и экспортом отчетов.*

## ⚡ Быстрый старт для новых пользователей

### 🎯 ГЛАВНАЯ ЦЕЛЬ ПРОГРАММЫ

**Помочь понять, нужно ли менять батарею в вашем MacBook**

Стандартные показатели macOS могут обманывать:

- Батарея показывает 5 часов работы, а садится за 2 часа  
- Заряд резко проваливается с 90% до 40%
- Система показывает "Нормально", но батарея перегревается

**BatMon выявляет такие проблемы и даёт чёткие рекомендации!**

## 🚀 Установка

### 📥 Способ 1: Go Install (для пользователей Go)

**Самый простой способ для тех, у кого установлен Go:**

```bash
go install github.com/region23/batmon@latest
```

Готово! BatMon установлен в `$GOPATH/bin/batmon` (или `~/go/bin/batmon`)

### 📦 Способ 2: Скачать готовый бинарник (РЕКОМЕНДУЕТСЯ)

1. **Перейдите на страницу релизов**: [GitHub Releases](https://github.com/region23/batmon/releases/latest)
2. **Скачайте архив для вашей платформы:**

   **🍎 macOS:**
   - Apple Silicon (M1/M2/M3): `batmon-darwin-arm64.tar.gz`
   - Intel: `batmon-darwin-amd64.tar.gz`

   **🐧 Linux:**
   - AMD64/x86_64: `batmon-linux-amd64.tar.gz`
   - ARM64: `batmon-linux-arm64.tar.gz`

   **🪟 Windows:**
   - AMD64/x86_64: `batmon-windows-amd64.zip`
   - ARM64: `batmon-windows-arm64.zip`

3. **Распакуйте архив:**

   ```bash
   # macOS/Linux
   tar -xzf batmon-*.tar.gz
   
   # Windows - разархивируйте zip
   ```

4. **Запустите:**

   ```bash
   ./batmon        # macOS/Linux
   batmon.exe      # Windows
   ```

### 🛠️ Способ 3: Сборка из исходного кода

```bash
git clone https://github.com/region23/batmon.git
cd batmon
go build -o batmon
./batmon
```

*Требуется Go 1.21+ (установите с [golang.org](https://golang.org/dl/))*

### 📋 КАК ПРАВИЛЬНО ПРОВЕСТИ ПОЛНЫЙ АНАЛИЗ

**ЭТО ОСНОВНОЙ СЦЕНАРИЙ ИСПОЛЬЗОВАНИЯ ПРОГРАММЫ:**

1. **Зарядите MacBook до 100%** 🔌
2. **Запустите программу** → выберите **"🔋 Полный анализ батареи (100% → 0%)"**
3. **Используйте MacBook как обычно** (работа, интернет, видео)
4. **Разрядите до 10-0%** (сохраните документы заранее!)
5. **Получите детальный отчет** с рекомендациями о замене

**⏱️ Время:** Минимум 2-3 часа для качественного анализа
**⚠️ Важно:** Не закрывайте программу во время теста!

## 👶 Пошаговая инструкция для новичков

**Если вы никогда не работали с Терминалом:**

### Шаг 1: Откройте Терминал

- 🔍 Нажмите `Cmd + Space` (Spotlight)
- 💬 Наберите "Терминал" или "Terminal"
- ⏎ Нажмите Enter

### Шаг 2: Установите Go (если еще нет)

```bash
# Проверьте, установлен ли Go
go version

# Если появилась ошибка, установите Go:
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
brew install go
```

### Шаг 3: Скачайте и запустите BatMon

```bash
# Скопируйте эту команду целиком и вставьте в Терминал:
git clone https://github.com/region23/batmon.git && cd batmon && go build -o batmon && ./batmon
```

### Шаг 4: Пользуйтесь программой

- 📊 В меню выберите `1` для интерактивного мониторинга
- ⌨️ Используйте `q` или `й` для выхода  
- 🔄 Используйте `r` или `к` для обновления данных
- 📖 Используйте `h` или `р` для справки

**🎉 Готово!** Теперь вы можете отслеживать состояние батареи MacBook!

### ❓ Частые вопросы новичков

**Q: Что делать, если появляется ошибка "command not found: git"?**  
A: Установите Xcode Command Line Tools: `xcode-select --install`

**Q: Что делать, если появляется ошибка "command not found: go"?**  
A: Установите Go через Homebrew: `brew install go`

**Q: Безопасно ли запускать эти команды?**  
A: Да, код открытый на GitHub, можете просмотреть перед установкой

**Q: Где сохраняются данные?**  
A: BatMon следует стандартам XDG Base Directory:

- **macOS**: `~/.local/share/batmon/batmon.sqlite`
- **Linux**: `~/.local/share/batmon/batmon.sqlite` (или `$XDG_DATA_HOME/batmon/`)
- **Windows**: `%LOCALAPPDATA%\batmon\batmon.sqlite`
- **Отчеты**: `~/Documents/` на всех платформах

**Q: Как удалить программу?**  
A: Удалите бинарник и папку с данными:

- **macOS/Linux**: `~/.local/share/batmon/`
- **Windows**: `%LOCALAPPDATA%\batmon\`

### 🛡️ Безопасность

- ✅ Код полностью открытый - можете проверить на [GitHub](https://github.com/region23/batmon)
- ✅ Программа только читает данные батареи - ничего не изменяет
- ✅ Никаких сетевых подключений - все работает локально
- ✅ Не требует прав администратора

Сделано @region23 с ❤️ для пользователей MacBook всех стран
