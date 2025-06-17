package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Domain models
type UserData struct {
	ID       string                    `json:"id"`
	Name     string                    `json:"name"`
	Crafts   map[string]int            `json:"crafts"`
	Refining map[string]map[string]int `json:"refining"` // Changed to support levels
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

// In-memory Git-based data store implementation
type GitMemoryDataStore struct {
	data      *GameData
	mutex     sync.RWMutex
	repo      *git.Repository
	storer    *memory.Storage
	filename  string
	repoURL   string
	username  string
	token     string
	branch    string
	syncEvery time.Duration
}

func NewGitMemoryDataStore(filename, repoURL, username, token string, syncIntervalMinutes int) (*GitMemoryDataStore, error) {
	storer := memory.NewStorage()
	fs := memfs.New() // Use go-billy/v5's memfs
	data := &GameData{Users: make(map[string]*UserData)}

	// Clone repository into memory with memfs
	repo, err := git.Clone(storer, fs, &git.CloneOptions{
		URL: repoURL,
		Auth: &http.BasicAuth{
			Username: username,
			Password: token,
		},
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName("main"),
	})
	if err != nil && err != git.ErrRepositoryNotExists {
		return nil, fmt.Errorf("failed to clone repository: %v", err)
	}

	// If repository exists, try to load game_data.json
	if err == nil {
		wt, err := repo.Worktree()
		if err != nil {
			return nil, fmt.Errorf("failed to get worktree: %v", err)
		}

		file, err := wt.Filesystem.Open(filename)
		if err == nil {
			defer file.Close()
			decoder := json.NewDecoder(file)
			if err := decoder.Decode(data); err != nil {
				return nil, fmt.Errorf("failed to decode game data: %v", err)
			}
		}
	}

	return &GitMemoryDataStore{
		data:      data,
		repo:      repo,
		storer:    storer,
		filename:  filename,
		repoURL:   repoURL,
		username:  username,
		token:     token,
		branch:    "main",
		syncEvery: time.Duration(syncIntervalMinutes) * time.Minute,
	}, nil
}

func (g *GitMemoryDataStore) LoadData() (*GameData, error) {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	// Return a deep copy to prevent external modifications
	dataCopy := &GameData{Users: make(map[string]*UserData)}
	for id, user := range g.data.Users {
		userCopy := &UserData{
			ID:       user.ID,
			Name:     user.Name,
			Crafts:   make(map[string]int),
			Refining: make(map[string]map[string]int),
		}
		for k, v := range user.Crafts {
			userCopy.Crafts[k] = v
		}
		for k, levels := range user.Refining {
			userCopy.Refining[k] = make(map[string]int)
			for l, v := range levels {
				userCopy.Refining[k][l] = v
			}
		}
		dataCopy.Users[id] = userCopy
	}
	return dataCopy, nil
}

func (g *GitMemoryDataStore) SaveData(data *GameData) error {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// Update in-memory data
	g.data = data

	return nil
}

func (g *GitMemoryDataStore) syncToGitHub() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// Serialize data
	jsonData, err := json.MarshalIndent(g.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal game data: %v", err)
	}

	// Get worktree
	wt, err := g.repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %v", err)
	}

	err = wt.Pull(&git.PullOptions{
		RemoteName: "origin",
		Auth: &http.BasicAuth{
			Username: g.username,
			Password: g.token,
		},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("can't pull remote: %v", err)
	}

	// Write file
	f, err := wt.Filesystem.Create(g.filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	if _, err := f.Write(jsonData); err != nil {
		f.Close()
		return fmt.Errorf("failed to write file: %v", err)
	}
	f.Close()

	// Check for changes
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("failed to get worktree status: %v", err)
	}
	if status.IsClean() {
		log.Println("No changes to commit")
		return nil // Skip commit and push
	}

	// Stage file
	if _, err := wt.Add(g.filename); err != nil {
		return fmt.Errorf("failed to stage file: %v", err)
	}

	// Commit changes
	commitMsg := fmt.Sprintf("Update %s at %s", g.filename, time.Now().Format(time.RFC3339))
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  g.username,
			Email: fmt.Sprintf("%s@users.noreply.github.com", g.username),
			When:  time.Now(),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to commit: %v", err)
	}

	// Push changes
	err = g.repo.Push(&git.PushOptions{
		Auth: &http.BasicAuth{
			Username: g.username,
			Password: g.token,
		},
		RefSpecs: []config.RefSpec{
			config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", g.branch, g.branch)),
		},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("failed to push: %v", err)
	}

	log.Printf("Successfully synced %s to GitHub", g.filename)
	return nil
}

func (g *GitMemoryDataStore) startSyncGoroutine() {
	ticker := time.NewTicker(g.syncEvery)
	go func() {
		for range ticker.C {
			if err := g.syncToGitHub(); err != nil {
				log.Printf("Failed to sync to GitHub: %v", err)
			}
		}
	}()
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

		// The command is extracted and arguments are parsed from the message.
		command := update.Message.Command()
		args := strings.Fields(update.Message.CommandArguments())

		var response string
		var err error

		rootCmd := createRootCmd(true)

		rootCmd.SetArgs(append([]string{command}, args...))

		// Execute the root command
		if err := rootCmd.Execute(); err != nil {
			response = fmt.Sprintf("Error: %v", err)
		}

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

	// New structure for craft categories
	craftCategoryOrder []string
	craftDisplayGroups map[string]string   // e.g., "mage" -> "🪄 Mage Tower"
	craftsByGroup      map[string][]string // e.g., "mage" -> ["marmor", "mboots", ...]
	craftItemNames     map[string]string   // e.g., "marmor" -> "🛡️ Armor"

	refineCategories map[string]string // e.g., "ore" -> "⛏️ Ore"
	refineTiers      []string          // e.g., ["I", "II", ...]
}

func NewGameCommandHandler(dataStore DataStore) *GameCommandHandler {
	return &GameCommandHandler{
		dataStore: dataStore,
		// Crafting categories setup
		craftCategoryOrder: []string{"mage", "hunter", "warrior"},
		craftDisplayGroups: map[string]string{
			"mage":    "🪄 Mage Tower",
			"hunter":  "🏹 Hunter Tower",
			"warrior": "⚔️ Warrior Tower",
		},
		craftsByGroup: map[string][]string{
			"mage":    {"marmor", "mboots", "mhelm", "firestaff", "holystaff", "hallowfall", "arcane", "frost", "curse", "moffhand"},
			"hunter":  {"harmor", "hboots", "hhelm", "bow", "dagger", "spear", "quarterstaff", "shapeshift", "druidstaff", "hoffhand"},
			"warrior": {"warmor", "wboots", "whelm", "sword", "axe", "mace", "hammer", "gloves", "crossbow", "shield"},
		},
		craftItemNames: map[string]string{
			"marmor":       "🛡️ mArmor",
			"mboots":       "👢 mBoots",
			"mhelm":        "👑 mHelm",
			"firestaff":    "🔥 Fire Staff",
			"holystaff":    "🌟 Holy Staff",
			"hallowfall":   "😇 Hallowfall",
			"arcane":       "🔮 Arcane",
			"frost":        "❄️ Frost",
			"curse":        "💀 Curse",
			"moffhand":     "📖 mOffhand",
			"harmor":       "🛡️ hArmor",
			"hboots":       "👢 hBoots",
			"hhelm":        "👑 hHelm",
			"bow":          "🏹 Bow",
			"dagger":       "🔪 Dagger",
			"spear":        "🔱 Spear",
			"quarterstaff": "🌲 Quarter Staff",
			"shapeshift":   "🐾 Shapeshift",
			"druidstaff":   "🌿 Druid Staff",
			"hoffhand":     "📖 hOffhand",
			"warmor":       "🛡️ wArmor",
			"wboots":       "👢 wBoots",
			"whelm":        "👑 wHelm",
			"sword":        "⚔️ Sword",
			"axe":          "🪓 Axe",
			"mace":         "🔨 Mace",
			"hammer":       "🔨 Hammer",
			"gloves":       "🥊 Gloves",
			"crossbow":     "🏹 Crossbow",
			"shield":       "🛡️ Shield",
		},
		// Refining setup
		refineCategories: map[string]string{
			"ore":    "⛏️ Ore",
			"skin":   "🦎 Skin",
			"cotton": "🌿 Cotton",
		},
		refineTiers: []string{"I", "II", "III", "IV", "V", "VI", "VII", "VIII"},
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

	if len(args) == 2 {
		craft := strings.ToLower(args[0])
		if _, exists := g.craftItemNames[craft]; !exists {
			return fmt.Sprintf("Unknown craft: %s. Use prefixed names like 'marmor', 'harmor', 'warmor'.", craft), nil
		}

		value, err := strconv.Atoi(args[1])
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

		return fmt.Sprintf("✅ Set %s to %d for %s", g.craftItemNames[craft], value, userName), nil
	}

	if len(args) == 1 {
		craft := strings.ToLower(args[0])
		if _, exists := g.craftItemNames[craft]; !exists {
			return fmt.Sprintf("Unknown craft: %s", craft), nil
		}
		return g.formatCraftRow(data, craft), nil
	}

	return "Usage: /craft [type]. To set value use /craft [type] [value]", nil
}

func (g *GameCommandHandler) HandleRefineCommand(userID, userName string, args []string) (string, error) {
	data, err := g.dataStore.LoadData()
	if err != nil {
		return "", err
	}

	if len(args) == 0 {
		return g.formatRefineTable(data), nil
	}

	if len(args) == 3 {
		refineType := strings.ToLower(args[0])
		if _, exists := g.refineCategories[refineType]; !exists {
			return fmt.Sprintf("Unknown refining type: %s", refineType), nil
		}

		levelStr := args[1]
		_, err := strconv.Atoi(levelStr) // Just to validate it's a number
		if err != nil {
			return "Invalid level. Please provide a number (e.g., 4 for Tier IV).", nil
		}

		value, err := strconv.Atoi(args[2])
		if err != nil {
			return "Invalid value. Please provide a number.", nil
		}

		if value < 0 || value > 100 {
			return "Value must be between 0 and 100.", nil
		}

		g.ensureUser(data, userID, userName)
		if data.Users[userID].Refining[refineType] == nil {
			data.Users[userID].Refining[refineType] = make(map[string]int)
		}
		data.Users[userID].Refining[refineType][levelStr] = value

		err = g.dataStore.SaveData(data)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("✅ Set %s T%s to %d for %s", g.refineCategories[refineType], levelStr, value, userName), nil
	}

	if len(args) == 1 {
		refineType := strings.ToLower(args[0])
		if _, exists := g.refineCategories[refineType]; !exists {
			return fmt.Sprintf("Unknown refining type: %s", refineType), nil
		}
		return g.formatRefineRow(data, refineType), nil
	}

	return "Usage: /refine [type]. To set value use /refine [type] [level] [value]", nil
}

func (g *GameCommandHandler) ensureUser(data *GameData, userID, userName string) {
	if data.Users[userID] == nil {
		data.Users[userID] = &UserData{
			ID:       userID,
			Name:     userName,
			Crafts:   make(map[string]int),
			Refining: make(map[string]map[string]int),
		}
	} else {
		// Update username in case it changed
		data.Users[userID].Name = userName
	}
}

func (g *GameCommandHandler) formatCraftTable(data *GameData) string {
	var result strings.Builder
	result.WriteString("```\n")

	userList := g.getSortedUsers(data)

	for _, groupKey := range g.craftCategoryOrder {
		result.WriteString(g.craftDisplayGroups[groupKey] + "\n")
		result.WriteString(fmt.Sprintf("%-20s", "Craft"))
		for _, user := range userList {
			result.WriteString(fmt.Sprintf("%-12.12s", user.Name))
		}
		result.WriteString("MAX\n")
		result.WriteString(strings.Repeat("-", 20+len(userList)*12+4) + "\n")

		for _, craftKey := range g.craftsByGroup[groupKey] {
			displayName := g.craftItemNames[craftKey]
			result.WriteString(fmt.Sprintf("%-20s", displayName))

			maxVal := 0
			for _, user := range userList {
				value := user.Crafts[craftKey]
				if value > maxVal {
					maxVal = value
				}
				if value == 0 {
					result.WriteString(fmt.Sprintf("%-12s", ""))
				} else {
					result.WriteString(fmt.Sprintf("%-12d", value))
				}
			}
			result.WriteString(fmt.Sprintf("%d\n", maxVal))
		}
		result.WriteString("\n")
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) formatCraftRow(data *GameData, craft string) string {
	displayName := g.craftItemNames[craft]

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

	userList := g.getSortedUsers(data)

	for refineKey, displayName := range g.refineCategories {
		result.WriteString("\n" + displayName + "\n")
		result.WriteString(fmt.Sprintf("%-10s", "Level"))
		for _, user := range userList {
			result.WriteString(fmt.Sprintf("%-12.12s", user.Name))
		}
		result.WriteString("MAX\n")
		result.WriteString(strings.Repeat("-", 10+len(userList)*12+4) + "\n")

		romanMap := map[string]string{"1": "I", "2": "II", "3": "III", "4": "IV", "5": "V", "6": "VI", "7": "VII", "8": "VIII"}

		for i := 4; i <= 8; i++ {
			levelStr := strconv.Itoa(i)
			roman, _ := romanMap[levelStr]
			result.WriteString(fmt.Sprintf("%-10s", roman))

			maxVal := 0
			for _, user := range userList {
				value := 0
				if user.Refining[refineKey] != nil {
					value = user.Refining[refineKey][levelStr]
				}

				if value > maxVal {
					maxVal = value
				}

				if value == 0 {
					result.WriteString(fmt.Sprintf("%-12s", ""))
				} else {
					result.WriteString(fmt.Sprintf("%-12d", value))
				}
			}
			result.WriteString(fmt.Sprintf("%d\n", maxVal))
		}
	}

	result.WriteString("```")
	return result.String()
}

func (g *GameCommandHandler) formatRefineRow(data *GameData, refineType string) string {
	displayName := g.refineCategories[refineType]
	var result strings.Builder
	result.WriteString(fmt.Sprintf("```\n%s Rankings:\n", displayName))

	type userValue struct {
		name  string
		value int
	}

	for i := 4; i <= 8; i++ {
		levelStr := strconv.Itoa(i)
		result.WriteString(fmt.Sprintf("\n--- TIER %s ---\n", levelStr))

		var userValues []userValue
		for _, user := range data.Users {
			if user.Refining[refineType] != nil {
				if value, exists := user.Refining[refineType][levelStr]; exists && value > 0 {
					userValues = append(userValues, userValue{user.Name, value})
				}
			}
		}

		if len(userValues) == 0 {
			result.WriteString("No data available\n")
			continue
		}

		sort.Slice(userValues, func(i, j int) bool {
			return userValues[i].value > userValues[j].value
		})

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
	viper.SetDefault("USER_ID_SALT", "change-this-salt-in-your-env-file")
	viper.SetDefault("GITHUB_REPO_URL", "")
	viper.SetDefault("GITHUB_USERNAME", "")
	viper.SetDefault("GITHUB_TOKEN", "")
	viper.SetDefault("SYNC_INTERVAL_MINUTES", 60)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			log.Printf("Error reading config file: %v", err)
		}
	}
}

// Cobra Commands
func createRootCmd(serverMode bool) *cobra.Command {
	var rootCmd = &cobra.Command{
		Use:   "albion-tracker",
		Short: "Albion Online craft and refining tracker",
		Long:  "A bot for tracking Albion Online crafting and refining levels.",
		// This makes sure cobra doesn't exit on error in bot mode
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	var serverModeArg bool
	rootCmd.PersistentFlags().BoolVarP(&serverModeArg, "server", "s", false, "Run in server mode for Telegram")

	// CLI/Bot commands
	var craftCmd = &cobra.Command{
		Use:   "craft [subcommand]",
		Short: "Manage craft data. Use 'm', 'h', 'w' prefixes for items (e.g. marmor, hboots).",
		RunE: func(cmd *cobra.Command, args []string) error {
			runCommand(serverMode, "craft", args)
			return nil
		},
	}

	var refineCmd = &cobra.Command{
		Use:   "refine [subcommand]",
		Short: "Manage refining data",
		RunE: func(cmd *cobra.Command, args []string) error {
			runCommand(serverMode, "refine", args)
			return nil
		},
	}

	// This allows `craft` and `refine` to be called without subcommands
	craftCmd.RunE = func(cmd *cobra.Command, args []string) error {
		runCommand(serverMode, "craft", args)
		return nil
	}

	refineCmd.RunE = func(cmd *cobra.Command, args []string) error {
		runCommand(serverMode, "refine", args)
		return nil
	}

	rootCmd.AddCommand(craftCmd, refineCmd)

	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if serverMode {
			runServer()
		} else {
			// In CLI mode, if no command is given, show help.
			return cmd.Help()
		}
		return nil
	}

	return rootCmd
}

// runCommand decides whether to run in server or CLI mode
func runCommand(isServer bool, command string, args []string) {
	if isServer {
		// Server logic is initiated from the main function's server flag
		return
	}

	runCLICommand(command, args)
}

func runServer() {
	token := viper.GetString("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required for server mode")
	}

	dataFile := viper.GetString("DATA_FILE")
	repoURL := viper.GetString("GITHUB_REPO_URL")
	username := viper.GetString("GITHUB_USERNAME")
	tokenGit := viper.GetString("GITHUB_TOKEN")
	syncInterval := viper.GetInt("SYNC_INTERVAL_MINUTES")

	dataStore, err := NewGitMemoryDataStore(dataFile, repoURL, username, tokenGit, syncInterval)
	if err != nil {
		log.Fatal("Failed to create data store:", err)
	}

	// Start sync goroutine
	dataStore.startSyncGoroutine()

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
	repoURL := viper.GetString("GITHUB_REPO_URL")
	username := viper.GetString("GITHUB_USERNAME")
	tokenGit := viper.GetString("GITHUB_TOKEN")
	syncInterval := viper.GetInt("SYNC_INTERVAL_MINUTES")

	dataStore, err := NewGitMemoryDataStore(dataFile, repoURL, username, tokenGit, syncInterval)
	if err != nil {
		log.Fatal("Failed to create data store:", err)
	}

	handler := NewGameCommandHandler(dataStore)
	cli := NewCLIClient(handler)

	cli.HandleCommand(command, args)

	// Sync immediately after CLI command
	if err := dataStore.syncToGitHub(); err != nil {
		log.Printf("Failed to sync to GitHub: %v", err)
	}
}

func main() {
	initConfig()

	// A special check for server mode that bypasses Cobra's usual execution flow
	// This allows the bot to run as a long-running process
	args := os.Args[1:]
	isServer := false
	for _, arg := range args {
		if arg == "-s" || arg == "--server" {
			isServer = true
			break
		}
	}

	if isServer {
		runServer()
	} else {
		// Run in CLI mode
		// We are not using the actions here, as the logic is now inside the command runners
		rootCmd := createRootCmd(isServer)

		if err := rootCmd.Execute(); err != nil {
			// We print the error here because SilenceErrors is on
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
	}
}
