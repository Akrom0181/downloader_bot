package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Constants for download limits
const (
	MaxFileSize       = 150 * 1024 * 1024 // 150MB for standard Telegram bots
	UpdateIntervalSec = 3                 // Progress update interval in seconds
)

// Download represents a download task
type Download struct {
	URL       string
	Platform  string
	Title     string
	Thumbnail string
	Progress  int
	IsAudio   bool
}

func main() {

	BotToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if BotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	bot, err := tgbotapi.NewBotAPI(BotToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// Map to store URL and download info by chat ID and message ID
	urlCache := make(map[string]Download)

	// Welcome message when bot starts or /start command is received
	welcomeMessage := `🚀 *Media Downloader*

Send any link from these platforms:
• YouTube
• Instagram 
• Facebook
• TikTok

I'll download the video or audio for you!`

	for update := range updates {
		if update.Message != nil {
			// Handle /start command
			if update.Message.Command() == "start" {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, welcomeMessage)
				msg.ParseMode = "Markdown"
				bot.Send(msg)
				continue
			}

			// Handle URLs
			if update.Message.Text != "" {
				url := update.Message.Text

				// Check if the text is a URL
				if isValidURL(url) {
					// Extract info from URL
					platform := detectPlatform(url)
					info := Download{
						URL:      url,
						Platform: platform,
						Progress: 0,
					}

					// Fetch video metadata
					go func() {
						title, thumbnail := getVideoInfo(url)
						info.Title = title
						info.Thumbnail = thumbnail

						// Store URL and info for callback reference
						cacheKey := getCacheKey(update.Message.Chat.ID, 0)
						urlCache[cacheKey] = info

						// Format platform icon
						platformIcon := getPlatformIcon(platform)

						// Send message with download options
						msg := tgbotapi.NewMessage(update.Message.Chat.ID,
							fmt.Sprintf("%s *%s*\n\n%s\n\nSelect download format:",
								platformIcon,
								platform,
								truncateString(info.Title, 200)))
						msg.ParseMode = "Markdown"
						msg.ReplyMarkup = createDownloadKeyboard(platform)
						sentMsg, _ := bot.Send(msg)

						// Update cache key with the actual message ID
						newCacheKey := getCacheKey(update.Message.Chat.ID, sentMsg.MessageID)
						urlCache[newCacheKey] = info
						delete(urlCache, cacheKey)

						// Send thumbnail if available
						if thumbnail != "" {
							photoMsg := tgbotapi.NewPhoto(update.Message.Chat.ID, tgbotapi.FileURL(thumbnail))
							photoMsg.ReplyToMessageID = sentMsg.MessageID
							bot.Send(photoMsg)
						}
					}()
				} else {
					bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID,
						"📎 Please send a valid URL from YouTube, Instagram, Facebook, or TikTok"))
				}
			}
		} else if update.CallbackQuery != nil {
			// Handle button callbacks
			callback := update.CallbackQuery
			cacheKey := getCacheKey(callback.Message.Chat.ID, callback.Message.MessageID)

			if info, ok := urlCache[cacheKey]; ok {
				parts := strings.Split(callback.Data, ":")

				if len(parts) == 2 {
					format := parts[0]
					quality := parts[1]

					// Acknowledge the callback
					bot.Request(tgbotapi.NewCallback(callback.ID, "Processing download..."))

					// Update info with audio flag
					info.IsAudio = (format == "audio")
					urlCache[cacheKey] = info

					// Edit message to show processing
					progressMsg := fmt.Sprintf("⏳ *Processing %s download*\n\n%s\n\n0%% complete...",
						quality, truncateString(info.Title, 150))

					editMsg := tgbotapi.NewEditMessageText(
						callback.Message.Chat.ID,
						callback.Message.MessageID,
						progressMsg,
					)
					editMsg.ParseMode = "Markdown"
					editMsg.ReplyMarkup = &tgbotapi.InlineKeyboardMarkup{}
					statusMsg, _ := bot.Send(editMsg)

					if format == "video" {
						go handleVideoDownload(bot, callback.Message.Chat.ID, info, quality, statusMsg.MessageID)
					} else if format == "audio" {
						go handleAudioDownload(bot, callback.Message.Chat.ID, info, statusMsg.MessageID)
					}
				}
			}
		}
	}
}

func getCacheKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

func isValidURL(url string) bool {
	// Basic URL validation
	return strings.HasPrefix(url, "http") &&
		(strings.Contains(url, "youtube.com") ||
			strings.Contains(url, "youtu.be") ||
			strings.Contains(url, "instagram.com") ||
			strings.Contains(url, "facebook.com") ||
			strings.Contains(url, "fb.com") ||
			strings.Contains(url, "tiktok.com"))
}

func detectPlatform(url string) string {
	lowerURL := strings.ToLower(url)
	switch {
	case strings.Contains(lowerURL, "youtube.com") || strings.Contains(lowerURL, "youtu.be"):
		return "YouTube"
	case strings.Contains(lowerURL, "instagram.com") || strings.Contains(lowerURL, "instagr.am"):
		return "Instagram"
	case strings.Contains(lowerURL, "facebook.com") || strings.Contains(lowerURL, "fb.com") ||
		strings.Contains(lowerURL, "fb.watch"):
		return "Facebook"
	case strings.Contains(lowerURL, "tiktok.com") || strings.Contains(lowerURL, "vm.tiktok.com"):
		return "TikTok"
	default:
		return "Unknown"
	}
}

func getPlatformIcon(platform string) string {
	switch platform {
	case "YouTube":
		return "📺"
	case "Instagram":
		return "📷"
	case "Facebook":
		return "👤"
	case "TikTok":
		return "🎵"
	default:
		return "🔗"
	}
}

func getVideoInfo(url string) (title string, thumbnail string) {
	// Get video title and thumbnail using yt-dlp
	cmd := exec.Command("yt-dlp", "--get-title", "--get-thumbnail", "--no-playlist", url)
	output, err := cmd.Output()

	if err != nil {
		log.Printf("Error getting video info: %v", err)
		return "Unknown Title", ""
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) >= 1 {
		title = lines[0]
	}
	if len(lines) >= 2 {
		thumbnail = lines[1]
	}

	return
}

func createDownloadKeyboard(platform string) tgbotapi.InlineKeyboardMarkup {
	switch platform {
	case "YouTube":
		return tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📹 360p", "video:360p"),
				tgbotapi.NewInlineKeyboardButtonData("📹 480p", "video:480p"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📹 720p", "video:720p"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔊 Audio MP3", "audio:mp3"),
			),
		)
	case "Instagram", "Facebook", "TikTok":
		return tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📹 Medium Quality Only", "video:medium"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔊 Audio Only", "audio:mp3"),
			),
		)
	default:
		return tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("📹 Best Quality", "video:best"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("🔊 Audio Only", "audio:mp3"),
			),
		)
	}
}

func handleVideoDownload(bot *tgbotapi.BotAPI, chatID int64, info Download, quality string, statusMsgID int) {
	// Create unique filename with timestamp
	timestamp := time.Now().UnixNano()
	videoOutput := fmt.Sprintf("video_%d.%%(ext)s", timestamp)
	// progressFile := fmt.Sprintf("progress_%d.txt", timestamp)

	// Set format code based on platform and quality
	var formatCode string

	switch {
	case info.Platform == "YouTube":
		switch quality {
		case "360p":
			formatCode = "18/bestvideo[height<=360]+bestaudio/best[height<=360]"
		case "480p":
			formatCode = "135+bestaudio/bestvideo[height<=480]+bestaudio/best[height<=480]"
		case "720p":
			formatCode = "22/136+bestaudio/bestvideo[height<=720]+bestaudio/best[height<=720]"
		default:
			formatCode = "best"
		}
	case info.Platform == "Instagram" || info.Platform == "Facebook" || info.Platform == "TikTok":
		switch quality {
		case "medium":
			formatCode = "worst[ext=mp4]/worst"
		default:
			formatCode = "best[ext=mp4]/best"
		}
	default:
		formatCode = "best"
	}

	// Build arguments for yt-dlp
	ytdlpArgs := []string{
		"-f", formatCode,
		"--remux-video", "mp4", // Add this line to ensure proper container format
		"-o", videoOutput,
		"--newline",
		"--progress-template", "%(progress.downloaded_bytes)s/%(progress.total_bytes)s",
		"--no-playlist",
	}

	// Add cookies for platforms that need authentication
	switch info.Platform {
	case "Instagram", "Facebook":
		ytdlpArgs = append(ytdlpArgs, "--no-check-certificate")
	}

	// Add the URL as the last argument
	ytdlpArgs = append(ytdlpArgs, info.URL)

	// Create command
	cmd := exec.Command("yt-dlp", ytdlpArgs...)

	// Set up progress tracking
	progressPipe, _ := cmd.StderrPipe()

	// Start the command
	err := cmd.Start()
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to start download process."))
		log.Println("Command start error:", err)
		return
	}

	// Read progress updates
	go trackProgress(bot, chatID, statusMsgID, progressPipe, info.Title, quality)

	// Wait for command to complete
	err = cmd.Wait()
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to download video."))
		log.Println("Download error:", err)
		return
	}

	// Find downloaded file
	videoFiles, _ := filepath.Glob(fmt.Sprintf("video_%d.*", timestamp))
	if len(videoFiles) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ No video file found after download completed."))
		return
	}
	videoFile := videoFiles[0]
	defer os.Remove(videoFile)

	// Get file info
	fileInfo, err := os.Stat(videoFile)
	if err != nil {
		log.Println("Failed to get file info:", err)
	}

	// Convert bytes to MB
	fileSizeMB := float64(fileInfo.Size()) / 1048576

	// Update the status message to indicate completion
	editMsg := tgbotapi.NewEditMessageText(
		chatID,
		statusMsgID,
		fmt.Sprintf("✅ *Download Complete!*\n\n%s\n\nUploading to Telegram...",
			truncateString(info.Title, 150)),
	)
	editMsg.ParseMode = "Markdown"
	bot.Send(editMsg)

	// Check if file is too large
	if fileInfo.Size() > MaxFileSize {
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("⚠️ Video file (%.1f MB) exceeds Telegram's limit. Try a lower quality option.", fileSizeMB)))
		return
	}

	// Format caption
	caption := fmt.Sprintf("📹 *%s* - %s\n▫️ Quality: %s\n▫️ Size: %.1f MB",
		info.Platform,
		truncateString(info.Title, 100),
		quality,
		fileSizeMB)

	// Send video
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(videoFile))
	video.Caption = caption
	video.ParseMode = "Markdown"
	if _, err := bot.Send(video); err != nil {
		log.Println("Failed to send video:", err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to send video. File might be too large for Telegram."))
	}
}

func handleAudioDownload(bot *tgbotapi.BotAPI, chatID int64, info Download, statusMsgID int) {
	// Create unique filename with timestamp
	timestamp := time.Now().UnixNano()
	audioOutput := fmt.Sprintf("audio_%d.%%(ext)s", timestamp)

	// Build command arguments
	ytdlpArgs := []string{
		"-x",
		"--audio-format", "mp3",
		"--audio-quality", "0",
		"-o", audioOutput,
		"--newline",
		"--progress-template", "%(progress.downloaded_bytes)s/%(progress.total_bytes)s",
		"--no-playlist",
	}

	// Add platform-specific options
	switch info.Platform {
	case "Instagram", "Facebook":
		ytdlpArgs = append(ytdlpArgs, "--no-check-certificate")
	}

	// Add URL as final argument
	ytdlpArgs = append(ytdlpArgs, info.URL)

	// Create command
	cmd := exec.Command("yt-dlp", ytdlpArgs...)

	// Set up progress tracking
	progressPipe, _ := cmd.StderrPipe()

	// Start the command
	err := cmd.Start()
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to start audio extraction process."))
		log.Println("Command start error:", err)
		return
	}

	// Read progress updates
	go trackProgress(bot, chatID, statusMsgID, progressPipe, info.Title, "MP3")

	// Wait for command to complete
	err = cmd.Wait()
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to extract audio."))
		log.Println("Audio extraction error:", err)
		return
	}

	// Find downloaded file
	audioFiles, _ := filepath.Glob(fmt.Sprintf("audio_%d.*", timestamp))
	if len(audioFiles) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ No audio file found after extraction completed."))
		return
	}
	audioFile := audioFiles[0]
	defer os.Remove(audioFile)

	// Get file info
	fileInfo, err := os.Stat(audioFile)
	if err != nil {
		log.Println("Failed to get file info:", err)
	}

	// Convert bytes to MB
	fileSizeMB := float64(fileInfo.Size()) / 1048576

	// Update the status message to indicate completion
	editMsg := tgbotapi.NewEditMessageText(
		chatID,
		statusMsgID,
		fmt.Sprintf("✅ *Audio Extraction Complete!*\n\n%s\n\nUploading to Telegram...",
			truncateString(info.Title, 150)),
	)
	editMsg.ParseMode = "Markdown"
	bot.Send(editMsg)

	// Check if file is too large
	if fileInfo.Size() > MaxFileSize {
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("⚠️ Audio file (%.1f MB) exceeds Telegram's limit.", fileSizeMB)))
		return
	}

	// Format caption
	caption := fmt.Sprintf("🎵 *%s* - %s\n▫️ Format: MP3\n▫️ Size: %.1f MB",
		info.Platform,
		truncateString(info.Title, 100),
		fileSizeMB)

	// Send audio
	audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(audioFile))
	audio.Caption = caption
	audio.ParseMode = "Markdown"
	audio.Title = info.Title
	if _, err := bot.Send(audio); err != nil {
		log.Println("Failed to send audio:", err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Failed to send audio. File might be too large for Telegram."))
	}
}

func trackProgress(bot *tgbotapi.BotAPI, chatID int64, statusMsgID int, progressReader io.Reader, title, quality string) {
	scanner := bufio.NewScanner(progressReader)
	lastUpdateTime := time.Now()

	for scanner.Scan() {
		line := scanner.Text()

		// Parse progress info from line
		progress := parseProgress(line)
		if progress > 0 && time.Since(lastUpdateTime).Seconds() >= UpdateIntervalSec {
			// Update message with progress
			editMsg := tgbotapi.NewEditMessageText(
				chatID,
				statusMsgID,
				fmt.Sprintf("⏳ *Processing %s download*\n\n%s\n\n%d%% complete...",
					quality, truncateString(title, 150), progress),
			)
			editMsg.ParseMode = "Markdown"
			bot.Send(editMsg)

			lastUpdateTime = time.Now()
		}
	}
}

func parseProgress(line string) int {
	// Example line: "123456789/987654321"
	parts := strings.Split(line, "/")
	if len(parts) != 2 {
		return 0
	}

	downloaded, err1 := strconv.ParseInt(parts[0], 10, 64)
	total, err2 := strconv.ParseInt(parts[1], 10, 64)

	if err1 != nil || err2 != nil || total == 0 {
		return 0
	}

	return int((float64(downloaded) / float64(total)) * 100)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
