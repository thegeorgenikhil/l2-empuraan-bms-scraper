package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/stealth"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type MovieDetails struct {
	Name     string `json:"name"`
	SlugName string `json:"slug_name"`
	Code     string `json:"code"`
	City     string `json:"city"`
	CityCode string `json:"city_code"`
	Date     string `json:"date"`
	Found    bool   `json:"found"`
}

type TelegramButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type TelegramKeyboard struct {
	InlineKeyboard [][]TelegramButton `json:"inline_keyboard"`
}

const (
	moviesFilename = "bms.json"
	logFilename    = "bms.log"
)

var (
	logger           = logrus.New()
	telegramBotToken string
	telegramChatID   string
	iftttWebhookAPI string
)

func init() {
	err := godotenv.Load()
	if err != nil {
		logger.Fatalf("Error loading .env file: %v", err)
	}

	// Load and validate required env vars
	telegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if telegramBotToken == "" {
		logger.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	telegramChatID = os.Getenv("TELEGRAM_CHAT_ID")
	if telegramChatID == "" {
		logger.Fatal("TELEGRAM_CHAT_ID environment variable not set")
	}

	iftttWebhookAPI = os.Getenv("IFTTT_WEBHOOK_API")
	if iftttWebhookAPI == "" {
		logger.Fatal("IFTTT_WEBHOOK_API environment variable not set")
	}

	logFile, err := os.OpenFile(logFilename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Fatalf("Error opening log file: %v", err)
	}
	logger.SetOutput(logFile)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	if _, err := launcher.NewBrowser().Get(); err != nil {
		logger.Fatalf("Error initializing browser: %v", err)
	}
}

func main() {
	startTime := time.Now()
	defer func() {
		if r := recover(); r != nil {
			logger.WithFields(logrus.Fields{
				"error": r,
			}).Error("Recovered from panic")

			stackBuf := make([]byte, 4096)
			stackSize := runtime.Stack(stackBuf, false)
			logger.WithFields(logrus.Fields{
				"stack_trace": string(stackBuf[:stackSize]),
				"goroutines":  runtime.NumGoroutine(),
			}).Error("Stack trace and goroutine info")
		}
	}()

	moviesList, err := loadMoviesFromJSON(moviesFilename)
	if err != nil {
		logger.WithError(err).Fatal("Error reading movies")
	}

	for i := range moviesList {
		if moviesList[i].Found {
			continue
		}

		browser := rod.New().Timeout(time.Minute * 1)
		if err := browser.Connect(); err != nil {
			logger.WithError(err).Fatal("Error connecting to browser")
		}
		defer browser.Close()	

		page := stealth.MustPage(browser)
		defer page.Close()	

		bookingURL := fmt.Sprintf("https://in.bookmyshow.com/buytickets/%s-%s/movie-%s-%s-MT/%s",
			moviesList[i].SlugName, moviesList[i].City, moviesList[i].CityCode, moviesList[i].Code, moviesList[i].Date)

		page.MustNavigate(bookingURL).MustWaitDOMStable()

		theatreContainer, err := page.Element(".sc-tk4ce6-2.kozbLe")
		if err != nil {
			logger.WithFields(logrus.Fields{
				"movie": moviesList[i].Name,
				"error": err,
			}).Error("Error finding theatre container")
			continue
		}

		theatreElements, err := theatreContainer.Elements(".sc-e8nk8f-3.iFKUFD")
		if err != nil {
			logger.WithFields(logrus.Fields{
				"movie": moviesList[i].Name,
				"error": err,
			}).Error("Error finding theatre elements")
			continue
		}

		if len(theatreElements) > 0 {
			moviesList[i].Found = true
			showDate := moviesList[i].Date
			formattedDate := fmt.Sprintf("%s-%s-%s", showDate[6:8], showDate[4:6], showDate[0:4])
			notificationMsg := fmt.Sprintf("🎬 *ALERT: Bookings Started!*\n\n🎥 Movie: *%s*\n📅 Date: *%s*\n🏟️ Found %d theatres in *%s*",
				moviesList[i].Name, formattedDate, len(theatreElements), cases.Title(language.English).String(moviesList[i].City))

			bookingKeyboard := TelegramKeyboard{
				InlineKeyboard: [][]TelegramButton{
					{
						{
							Text: "🎟️ Book Now",
							URL:  bookingURL,
						},
					},
				},
			}

			err = sendTelegramNotification(telegramChatID, notificationMsg, "Markdown", bookingKeyboard)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"movie": moviesList[i].Name,
					"error": err,
				}).Error("Error sending Telegram notification")
			}

			err = sendIFTTTVoipCall(moviesList[i].Name)
			if err != nil {
				logger.WithFields(logrus.Fields{
					"movie": moviesList[i].Name,
					"error": err,
				}).Error("Error sending IFTTT VoIP call")
			}

			logger.WithFields(logrus.Fields{
				"movie": moviesList[i].Name,
				"date": formattedDate,
				"theatres": len(theatreElements),
				"city": moviesList[i].City,
				"url": bookingURL,
			}).Info("Found movie with available bookings")
		}
	}

	if err := saveMoviesToJSON(moviesFilename, moviesList); err != nil {
		logger.WithError(err).Error("Error saving final state to JSON")
	}

	duration := time.Since(startTime)
	logger.WithField("duration_in_seconds", duration.Seconds()).Info("cron completed")
}

func loadMoviesFromJSON(filename string) ([]MovieDetails, error) {
	fileData, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading file %s: %v", filename, err)
	}

	var moviesList []MovieDetails
	if err := json.Unmarshal(fileData, &moviesList); err != nil {
		return nil, fmt.Errorf("error unmarshaling JSON: %v", err)
	}

	return moviesList, nil
}

func saveMoviesToJSON(filename string, moviesList []MovieDetails) error {
	jsonData, err := json.MarshalIndent(moviesList, "", "    ")
	if err != nil {
		return fmt.Errorf("error marshaling JSON: %v", err)
	}

	if err := os.WriteFile(filename, jsonData, 0644); err != nil {
		return fmt.Errorf("error writing file %s: %v", filename, err)
	}
	return nil
}

func sendTelegramNotification(chatID string, message string, parseMode string, keyboard TelegramKeyboard) error {
	payload := map[string]interface{}{
		"chat_id":      chatID,
		"text":         message,
		"parse_mode":   parseMode,
		"reply_markup": keyboard,
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshaling payload: %v", err)
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", telegramBotToken)
	response, err := http.Post(apiURL, "application/json", bytes.NewBuffer(payloadJSON))
	if err != nil {
		return fmt.Errorf("error making telegram request: %v", err)
	}
	defer response.Body.Close()

	var apiResponse struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(response.Body).Decode(&apiResponse); err != nil {
		return fmt.Errorf("error decoding response: %v", err)
	}

	if !apiResponse.Ok {
		return fmt.Errorf("telegram API error: %s", apiResponse.Description)
	}

	return nil
}

func sendIFTTTVoipCall(value string) error {
	payload := map[string]string{
		"value1": value,
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshaling payload: %v", err)
	}

	response, err := http.Post(iftttWebhookAPI, "application/json", bytes.NewBuffer(payloadJSON))
	if err != nil {
		return fmt.Errorf("error making IFTTT request: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %v", err)
	}

	logger.WithField("response", string(body)).Info("got response from IFTTT")

	return nil
}