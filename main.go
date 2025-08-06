package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/stealth"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
)

type MovieDetails struct {
	Name     string   `json:"name"`
	SlugName string   `json:"slug_name"`
	Code     string   `json:"code"`
	City     string   `json:"city"`
	CityCode string   `json:"city_code"`
	Date     string   `json:"date"`
	Found    bool     `json:"found"`
	Theatres []string `json:"theatres"`
}

type TheatreDetails struct {
	Name      string `json:"name"`
	ShowCount int    `json:"show_count"`
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

		bookingURL := fmt.Sprintf("https://in.bookmyshow.com/movies/%s/%s/buytickets/%s/%s",
			moviesList[i].City, moviesList[i].SlugName, moviesList[i].Code, moviesList[i].Date)


		page.MustNavigate(bookingURL).MustWaitDOMStable()

		theatreContainer, err := page.Element(".ReactVirtualized__Grid__innerScrollContainer")
		if err != nil {
			logger.WithFields(logrus.Fields{
				"movie": moviesList[i].Name,
				"error": err,
			}).Error("Error finding theatre container")
			continue
		}

		theatreElements, err := theatreContainer.Elements(".sc-e8nk8f-3.hStBrg")
		if err != nil {
			logger.WithFields(logrus.Fields{
				"movie": moviesList[i].Name,
				"error": err,
			}).Error("Error finding theatre elements")
			continue
		}

		var theatreDetails []TheatreDetails
		if len(theatreElements) > 0 {
			for _, theatreEl := range theatreElements {
				theatreNameDiv, _ := theatreEl.Element(".sc-1qdowf4-0.fbRYHb")
				theatreShowsEl, _ := theatreEl.Elements(".sc-1la7659-0.bLMTPx")
				theatreName, _ := theatreNameDiv.Text()
				theatreDetails = append(theatreDetails, TheatreDetails{
					Name:      theatreName,
					ShowCount: len(theatreShowsEl),
				})
			}
		}

		var newTheatres []TheatreDetails
		for _, theatre := range theatreDetails {
			if theatre.Name == "" {
				continue
			}

			if !slices.Contains(moviesList[i].Theatres, theatre.Name) {
				moviesList[i].Theatres = append(moviesList[i].Theatres, theatre.Name)
				newTheatres = append(newTheatres, theatre)
			}
		}

		if len(newTheatres) > 0 {
			showDate := moviesList[i].Date
			formattedDate := fmt.Sprintf("%s-%s-%s", showDate[6:8], showDate[4:6], showDate[0:4])

			for _, theatre := range newTheatres {
				notificationMsg := fmt.Sprintf("🎬 *New Show Added!*\n\n🎥 Movie: *%s*\n📅 Date: *%s*\n🏟️ Theatre: *%s*\nShows: *%d*",
					moviesList[i].Name, formattedDate, theatre.Name, theatre.ShowCount)

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
						"movie":   moviesList[i].Name,
						"theatre": theatre.Name,
						"error":   err,
					}).Error("Error sending Telegram notification")
				}

				logger.WithFields(logrus.Fields{
					"movie":   moviesList[i].Name,
					"date":    formattedDate,
					"theatre": theatre.Name,
					"shows":   theatre.ShowCount,
					"url":     bookingURL,
				}).Info("Found new show")
			}
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