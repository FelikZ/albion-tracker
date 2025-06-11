package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Domain models
type UserData struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Crafts   map[string]int `json:"crafts"`
	Refining map[string]int `json:"refining"`
}

type GameData struct {
	Users map[string]*UserData `json:"users"`
}

// Interfaces for SOLID design
type MessengerClient interface {
	SendMessage(chatID int64, text string) error
	Start() error
	Stop() error
}

type DataStore interface {
	LoadData() (*GameData, error)
	SaveData(*GameData) error
}

type CommandHandler interface {
	HandleCraftCommand(userID, userName string, args []string) (string, error)
	HandleRefineCommand(userID, userName string, args []string) (string, error)
}

// File-based data store implementation
type FileDataStore struct {
	filename string
}

func NewFileDataStore(filename string) *FileDataStore {
	return &FileDataStore{filename: filename}
}

func (f *FileDataStore) LoadData() (*GameData, error) {
	data := &GameData{Users: make(map[string]*UserData)}

	if _, err := os.Stat(f.filename); os.IsNotExist(err) {
		return data, nil
	}

	file, err := ioutil.ReadFile(f.filename)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(file, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (f *FileDataStore) SaveData(data *GameData) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return ioutil.WriteFile(f.filename, jsonData, 0644)
}

// Telegram messenger client implementation
type TelegramClient struct {
	bot     *tgbotapi.BotAPI
	handler CommandHandler
}

func NewTelegramClient(token string, handler CommandHandler) (*TelegramClient, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	return &TelegramClient{
		bot:     bot,
		handler: handler,
	}, nil
}

func (t *TelegramClient) SendMessage(chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	_, err := t.bot.Send(msg)
	return err
}

func (t *TelegramClient) Start() error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := t.bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		if !update.Message.IsCommand() {
			continue
		}

		userID := generateUserID(strconv.FormatInt(update.Message.From.ID, 10))
		userName := update.Message.From.UserName
		if userName == "" {
			userName = update.Message.From.FirstName
		}

		command := update.Message.Command()
		args := strings.Fields(update.Message.CommandArguments())

		var response string
		var err error

		switch command {
		case "craft":
			response, err = t.handler.HandleCraftCommand(userID, userName, args)
		case "refine":
			response, err = t.handler.HandleRefineCommand(userID, userName, args)
		default:
			response = "Unknown command. Available commands: /craft, /refine"
		}

		if err != nil {
			response = fmt.Sprintf("Error: %v", err)
		}

		t.SendMessage(update.Message.Chat.ID, response)
	}

	return nil
}

func (t *TelegramClient) Stop() error {
	t.bot.StopReceivingUpdates()
	return nil
}

// Command handler implementation
type GameCommandHandler struct {
	dataStore DataStore

	// Craft categories with their display names and emojis
	craftCategories  map[string]string
	refineCategories map[string]string
}

func NewGameCommandHandler(dataStore DataStore) *GameCommandHandler {
	return &GameCommandHandler{
		dataStore: dataStore,
		craftCategories: map[string]string{
			"armor":        "🛡️ Armor",
			"boots":        "👢 Boots",
			"helm":         "👑 Helm",
			"firestaff":    "🔥 Fire Staff",
			"holystaff":    "🌟 Holy Staff",
			"arcane":       "🔮 Arcane",
			"frost":        "❄️ Frost",
			"curse":        "💀 Curse",
			"offhand":      "📖 Offhand",
			"bow":          "🏹 Bow",
			"leatherboots": "👢 Leather Boots",
			"dagger":       "🔪 Dagger",
			"spear":        "🔱 Spear",
			"quarterstaff": "🌲 Quarterstaff",
			"shapeshift":   "🐾 Shapeshift",
			"druidstaff":   "🌿 Druid Staff",
			"sword":        "⚔️ Sword",
			"axe":          "🪓 Axe",
			"mace":         "🔨 Mace",
			"hammer":       "🔨 Hammer",
			"gloves":       "🥊 Gloves",
			"crossbow":     "🏹 Crossbow",
			"shield":       "🛡️ Shield",
		},
		refineCategories: map[string]string{
			"ore":    "⛏️ Ore",
			"skin":   "🦎 Skin",
			"cotton": "🌿 Cotton",
		},
	}
}

func (g *GameCommandHandler) HandleCraftCommand(userID, userName string, args []string) (string, error) {
	data, err := g.dataStore.LoadData()
	if err != nil {
		return "", err
	}

	if len(args) == 0 {
		return g.formatCraftTable(data), nil
	}

	if len(args) == 1 {
		craft := strings.ToLower(args[0])
		if _, exists := g.craftCategories[craft]; !exists {
			return fmt.Sprintf("Unknown craft: %s", craft), nil
		}
		return g.formatCraftRow(data, craft), nil
	}

	if len(args) == 3 && args[0] == "set" {
		craft := strings.ToLower(args[1])
		if _, exists := g.craftCategories[craft]; !exists {
			return fmt.Sprintf("Unknown craft: %s", craft), nil
		}

		value, err := strconv.Atoi(args[2])
		if err != nil {
			return "Invalid value. Please provide a number.", nil
		}

		if value < 0 || value > 100 {
			return "Value must be between 0 and 100.", nil
		}

		g.ensureUser(data, userID, userName)
		data.Users[userID].Crafts[craft] = value

		err = g.dataStore.SaveData(data)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("✅ Set %s to %d for %s", g.craftCategories[craft], value, userName), nil
	}

	return "Usage: /craft [craft_name] or /craft set [craft_name] [value]", nil
}

func (g *GameCommandHandler) HandleRefineCommand(userID, userName string, args []string) (string, error) {
	data, err := g.dataStore.LoadData()
	if err != nil {
		return "", err
	}

	if len(args) == 0 {
		return g.formatRefineTable(data), nil
	}

	if len(args) == 1 {
		refine := strings.ToLower(args[0])
		if _, exists := g.refineCategories[refine]; !exists {
			return fmt.Sprintf("Unknown refining: %s", refine), nil
		}
		return g.formatRefineRow(data, refine), nil
	}

	if len(args) == 3 && args[0] == "set" {
		refine := strings.ToLower(args[1])
		if _, exists := g.refineCategories[refine]; !exists {
			return fmt.Sprintf("Unknown refining: %s", refine), nil
		}

		value, err := strconv.Atoi(args[2])
		if err != nil {
			return "Invalid value. Please provide a number.", nil
		}

		if value < 0 || value > 100 {
			return "Value must be between 0 and 100.", nil
		}

		g.ensureUser(data, userID, userName)
		data.Users[userID].Refining[refine] = value

		err = g.dataStore.SaveData(data)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("✅ Set %s to %d for %s", g.refineCategories[refine], value, userName), nil
	}

	return "Usage: /refine [refine_type] or /refine set [refine_type] [value]", nil
}

func (g *GameCommandHandler) ensureUser(data *GameData, userID, userName string) {
	if data.Users[userID] == nil {
		data.Users[userID] = &UserData{
			ID:       userID,
			Name:     userName,
			Crafts:   make(map[string]int),
			Refining: make(map[string]int),
		}
	} else {
		// Update username in case it changed
		data.Users[userID].Name = userName
	}
}

func (g *GameCommandHandler) formatCraftTable(data *GameData) string {
	var result strings.Builder
	result.WriteString("```\n🪄 CRAFT TABLE\n")
	result.WriteString(strings.Repeat("=", 50) + "\n")

	// Header with usernames
	result.WriteString(fmt.Sprintf("%-20s", "Craft"))
	userList := g.getSortedUsers(data)
	for _, user := range userList {
		result.WriteString(fmt.Sprintf("%-12s", user.Name))
	}
	result.WriteString("MAX\n")
	result.WriteString(strings.Repeat("-", 50) + "\n")

	// Craft rows
	for craft, displayName := range g.craftCategories {
		result.WriteString(fmt.Sprintf("%-20s", displayName))

		values := make([]int, 0)
		for _, user := range userList {
			value := user.Crafts[craft]
			result.WriteString(fmt.Sprintf("%-12d", value))
			if value > 0 {
				values = append(values, value)
			}
		}

		maxVal := 0
		if len(values) > 0 {
			sort.Sort(sort.Reverse(sort.IntSlice(values)))
			maxVal = values[0]
		}
		result.WriteString(fmt.Sprintf("%d\n", maxVal))
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) formatCraftRow(data *GameData, craft string) string {
	displayName := g.craftCategories[craft]

	type userValue struct {
		name  string
		value int
	}

	var userValues []userValue
	for _, user := range data.Users {
		if value, exists := user.Crafts[craft]; exists && value > 0 {
			userValues = append(userValues, userValue{user.Name, value})
		}
	}

	// Sort by value descending
	sort.Slice(userValues, func(i, j int) bool {
		return userValues[i].value > userValues[j].value
	})

	var result strings.Builder
	result.WriteString(fmt.Sprintf("```\n%s Rankings:\n", displayName))
	result.WriteString(strings.Repeat("=", 30) + "\n")

	if len(userValues) == 0 {
		result.WriteString("No data available\n")
	} else {
		for i, uv := range userValues {
			medal := ""
			switch i {
			case 0:
				medal = "🥇"
			case 1:
				medal = "🥈"
			case 2:
				medal = "🥉"
			default:
				medal = fmt.Sprintf("%d.", i+1)
			}
			result.WriteString(fmt.Sprintf("%s %-15s %d\n", medal, uv.name, uv.value))
		}
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) formatRefineTable(data *GameData) string {
	var result strings.Builder
	result.WriteString("```\n⛏️ REFINING TABLE\n")
	result.WriteString(strings.Repeat("=", 50) + "\n")

	// Header with usernames
	result.WriteString(fmt.Sprintf("%-20s", "Refining"))
	userList := g.getSortedUsers(data)
	for _, user := range userList {
		result.WriteString(fmt.Sprintf("%-12s", user.Name))
	}
	result.WriteString("MAX\n")
	result.WriteString(strings.Repeat("-", 50) + "\n")

	// Refining rows
	for refine, displayName := range g.refineCategories {
		result.WriteString(fmt.Sprintf("%-20s", displayName))

		values := make([]int, 0)
		for _, user := range userList {
			value := user.Refining[refine]
			result.WriteString(fmt.Sprintf("%-12d", value))
			if value > 0 {
				values = append(values, value)
			}
		}

		maxVal := 0
		if len(values) > 0 {
			sort.Sort(sort.Reverse(sort.IntSlice(values)))
			maxVal = values[0]
		}
		result.WriteString(fmt.Sprintf("%d\n", maxVal))
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) formatRefineRow(data *GameData, refine string) string {
	displayName := g.refineCategories[refine]

	type userValue struct {
		name  string
		value int
	}

	var userValues []userValue
	for _, user := range data.Users {
		if value, exists := user.Refining[refine]; exists && value > 0 {
			userValues = append(userValues, userValue{user.Name, value})
		}
	}

	// Sort by value descending
	sort.Slice(userValues, func(i, j int) bool {
		return userValues[i].value > userValues[j].value
	})

	var result strings.Builder
	result.WriteString(fmt.Sprintf("```\n%s Rankings:\n", displayName))
	result.WriteString(strings.Repeat("=", 30) + "\n")

	if len(userValues) == 0 {
		result.WriteString("No data available\n")
	} else {
		for i, uv := range userValues {
			medal := ""
			switch i {
			case 0:
				medal = "🥇"
			case 1:
				medal = "🥈"
			case 2:
				medal = "🥉"
			default:
				medal = fmt.Sprintf("%d.", i+1)
			}
			result.WriteString(fmt.Sprintf("%s %-15s %d\n", medal, uv.name, uv.value))
		}
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) getSortedUsers(data *GameData) []*UserData {
	users := make([]*UserData, 0, len(data.Users))
	for _, user := range data.Users {
		users = append(users, user)
	}

	// Sort by name for consistent ordering
	sort.Slice(users, func(i, j int) bool {
		return users[i].Name < users[j].Name
	})

	return users
}

// CLI implementation
type CLIClient struct {
	handler CommandHandler
}

func NewCLIClient(handler CommandHandler) *CLIClient {
	return &CLIClient{handler: handler}
}

func (c *CLIClient) HandleCommand(command string, args []string) {
	// For CLI, use a dummy user ID and name
	userID := generateUserID("cli-user")
	userName := "CLI-User"

	var response string
	var err error

	switch command {
	case "craft":
		response, err = c.handler.HandleCraftCommand(userID, userName, args)
	case "refine":
		response, err = c.handler.HandleRefineCommand(userID, userName, args)
	default:
		response = "Unknown command. Available commands: craft, refine"
	}

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println(response)
}

// Utility functions
func generateUserID(messengerUserID string) string {
	salt := viper.GetString("USER_ID_SALT")
	if salt == "" {
		salt = "default-salt" // Should be set via config
	}

	hash := sha256.Sum256([]byte(messengerUserID + salt))
	return fmt.Sprintf("%x", hash)[:16] // Use first 16 chars
}

func initConfig() {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	// Set defaults
	viper.SetDefault("DATA_FILE", "game_data.json")
	viper.SetDefault("USER_ID_SALT", "change-this-salt")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Printf("Error reading config file: %v", err)
		}
	}
}

// CLI Commands
func createRootCmd() *cobra.Command {
	var rootCmd = &cobra.Command{
		Use:   "albion-tracker",
		Short: "Albion Online craft and refining tracker",
		Long:  "A bot for tracking Albion Online crafting and refining levels",
	}

	// Server command
	var serverCmd = &cobra.Command{
		Use:   "server",
		Short: "Start the Telegram bot server",
		Run: func(cmd *cobra.Command, args []string) {
			runServer()
		},
	}

	// CLI commands
	var craftCmd = &cobra.Command{
		Use:   "craft [subcommand]",
		Short: "Manage craft data",
		Run: func(cmd *cobra.Command, args []string) {
			runCLICommand("craft", args)
		},
	}

	var refineCmd = &cobra.Command{
		Use:   "refine [subcommand]",
		Short: "Manage refining data",
		Run: func(cmd *cobra.Command, args []string) {
			runCLICommand("refine", args)
		},
	}

	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(craftCmd)
	rootCmd.AddCommand(refineCmd)

	return rootCmd
}

func runServer() {
	token := viper.GetString("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}

	dataFile := viper.GetString("DATA_FILE")
	dataStore := NewFileDataStore(dataFile)
	handler := NewGameCommandHandler(dataStore)

	client, err := NewTelegramClient(token, handler)
	if err != nil {
		log.Fatal("Failed to create Telegram client:", err)
	}

	log.Println("Starting Telegram bot server...")
	if err := client.Start(); err != nil {
		log.Fatal("Failed to start server:", err)
	}
}

func runCLICommand(command string, args []string) {
	dataFile := viper.GetString("DATA_FILE")
	dataStore := NewFileDataStore(dataFile)
	handler := NewGameCommandHandler(dataStore)
	cli := NewCLIClient(handler)

	cli.HandleCommand(command, args)
}

func main() {
	initConfig()

	rootCmd := createRootCmd()
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}
