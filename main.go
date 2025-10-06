func (m model) viewAdminProgress() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	s := titleStyle.Render("Completion Progress") + "\n\n"

	appData.mu.RLock()
	defer appData.mu.RUnlock()

	if len(appData.TodoOrder) == 0 {
		s += "No todo items.\n"
		return s
	}

	// Header
	s += fmt.Sprintf("%-20s", "User")
	for _, todoID := range appData.TodoOrder {
		todo := appData.TodoItems[todoID]
		name := todo.Name
		if len(name) > 10 {
			name = name[:10]
		}
		s += fmt.Sprintf("%-12s", name)
	}
	s += "\n" + strings.Repeat("-", 20+12*len(appData.TodoOrder)) + "\n"

	// Rows
	for _, user := range appData.Users {
		if user.IsAdmin {
			continue
		}
		username := user.Username
		if len(username) > 20 {
			username = username[:20]
		}
		s += fmt.Sprintf("%-20s", username)

		for _, todoID := range appData.TodoOrder {
			if user.Completions[todoID] {
				s += greenStyle.Render(fmt.Sprintf("%-12s", "✓"))
			} else {
				s += fmt.Sprintf("%-12s", "-")
			}
		}
		s += "\n"
	}

	s += "\n" + lipgloss.NewStyle().Faint(true).Render("Esc: back")
	return s
}

func (m model) viewAdminDeleteConfirm() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	warningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	s := titleStyle.Render("Confirm Delete") + "\n\n"

	appData.mu.RLock()
	todo := appData.TodoItems[m.deleteConfirmID]
	appData.mu.RUnlock()

	s += warningStyle.Render(fmt.Sprintf("Delete '%s'?", todo.Name)) + "\n\n"
	s += "This will remove the todo item and all completion records.\n\n"
	s += lipgloss.NewStyle().Faint(true).Render("y: confirm • n/Esc: cancel")

	return s
}package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

const (
	host     = "0.0.0.0"
	port     = 2222
	dataFile = "checklist_data.json"
)

// Data models
type TodoItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type User struct {
	Username   string            `json:"username"`
	PublicKey  string            `json:"public_key"`
	IsAdmin    bool              `json:"is_admin"`
	Completions map[string]bool  `json:"completions"` // TodoID -> completed
}

type AppData struct {
	Users     map[string]*User    `json:"users"`      // PublicKey -> User
	TodoItems map[string]*TodoItem `json:"todo_items"` // ID -> TodoItem
	TodoOrder []string            `json:"todo_order"` // Ordered list of IDs
	mu        sync.RWMutex
}

var appData = &AppData{
	Users:     make(map[string]*User),
	TodoItems: make(map[string]*TodoItem),
	TodoOrder: []string{},
}

// TUI Models
type viewMode int

const (
	usernameView viewMode = iota
	todoListView
	todoDetailView
	adminView
	adminEditView
	adminProgressView
	adminDeleteConfirmView
)

type model struct {
	user        *User
	publicKey   string
	mode        viewMode
	cursor      int
	input       string
	err         error
	width       int
	height      int

	// Admin edit state
	editingID   string
	editField   int // 0=name, 1=description
	editName    string
	editDesc    string

	// Todo detail state
	selectedTodo *TodoItem

	// Delete confirmation state
	deleteConfirmID string
}

func initialModel(publicKey string) model {
	appData.mu.RLock()
	user := appData.Users[publicKey]
	appData.mu.RUnlock()

	m := model{
		publicKey: publicKey,
		user:      user,
		width:     80,
		height:    24,
	}

	if user == nil {
		m.mode = usernameView
	} else if user.IsAdmin {
		m.mode = adminView
	} else {
		m.mode = todoListView
	}

	return m
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch m.mode {
		case usernameView:
			return m.updateUsernameView(msg)
		case todoListView:
			return m.updateTodoListView(msg)
		case todoDetailView:
			return m.updateTodoDetailView(msg)
		case adminView:
			return m.updateAdminView(msg)
		case adminEditView:
			return m.updateAdminEditView(msg)
		case adminProgressView:
			return m.updateAdminProgressView(msg)
		case adminDeleteConfirmView:
			return m.updateAdminDeleteConfirmView(msg)
		}
	}
	return m, nil
}

func (m model) updateUsernameView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyEnter:
		if m.input != "" {
			appData.mu.Lock()
			isFirstUser := len(appData.Users) == 0
			user := &User{
				Username:   m.input,
				PublicKey:  m.publicKey,
				IsAdmin:    isFirstUser,
				Completions: make(map[string]bool),
			}
			appData.Users[m.publicKey] = user
			saveData()
			appData.mu.Unlock()

			m.user = user
			if isFirstUser {
				m.mode = adminView
			} else {
				m.mode = todoListView
			}
			m.input = ""
		}
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			m.input = m.input[:len(m.input)-1]
		}
	default:
		if len(msg.String()) == 1 && len(m.input) < 20 {
			m.input += msg.String()
		}
	}
	return m, nil
}

func (m model) updateTodoListView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	appData.mu.RLock()
	itemCount := len(appData.TodoOrder)
	appData.mu.RUnlock()

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < itemCount-1 {
			m.cursor++
		}
	case "enter":
		if itemCount > 0 && m.cursor < itemCount {
			appData.mu.RLock()
			todoID := appData.TodoOrder[m.cursor]
			m.selectedTodo = appData.TodoItems[todoID]
			appData.mu.RUnlock()
			m.mode = todoDetailView
		}
	}
	return m, nil
}

func (m model) updateTodoDetailView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.mode = todoListView
		m.selectedTodo = nil
	case "c", "enter":
		if m.selectedTodo != nil {
			appData.mu.Lock()
			m.user.Completions[m.selectedTodo.ID] = true
			saveData()
			appData.mu.Unlock()
			m.mode = todoListView
			m.selectedTodo = nil
		}
	}
	return m, nil
}

func (m model) updateAdminView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	appData.mu.RLock()
	itemCount := len(appData.TodoOrder)
	appData.mu.RUnlock()

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < itemCount-1 {
			m.cursor++
		}
	case "K": // Move item up
		if itemCount > 0 && m.cursor > 0 {
			appData.mu.Lock()
			appData.TodoOrder[m.cursor], appData.TodoOrder[m.cursor-1] =
				appData.TodoOrder[m.cursor-1], appData.TodoOrder[m.cursor]
			saveData()
			appData.mu.Unlock()
			m.cursor--
		}
	case "J": // Move item down
		if itemCount > 0 && m.cursor < itemCount-1 {
			appData.mu.Lock()
			appData.TodoOrder[m.cursor], appData.TodoOrder[m.cursor+1] =
				appData.TodoOrder[m.cursor+1], appData.TodoOrder[m.cursor]
			saveData()
			appData.mu.Unlock()
			m.cursor++
		}
	case "a":
		m.mode = adminEditView
		m.editingID = ""
		m.editField = 0
		m.editName = ""
		m.editDesc = ""
	case "e":
		if itemCount > 0 && m.cursor < itemCount {
			appData.mu.RLock()
			todoID := appData.TodoOrder[m.cursor]
			todo := appData.TodoItems[todoID]
			appData.mu.RUnlock()

			m.mode = adminEditView
			m.editingID = todoID
			m.editField = 0
			m.editName = todo.Name
			m.editDesc = todo.Description
		}
	case "d":
		if itemCount > 0 && m.cursor < itemCount {
			appData.mu.RLock()
			todoID := appData.TodoOrder[m.cursor]
			appData.mu.RUnlock()

			m.deleteConfirmID = todoID
			m.mode = adminDeleteConfirmView
		}
	case "p":
		m.mode = adminProgressView
		m.cursor = 0
	}
	return m, nil
}

func (m model) updateAdminEditView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = adminView
	case "ctrl+c":
		return m, tea.Quit
	case "tab":
		m.editField = (m.editField + 1) % 2
	case "enter", "ctrl+s":
		appData.mu.Lock()
		if m.editingID == "" {
			// New item
			id := fmt.Sprintf("todo_%d", time.Now().Unix())
			appData.TodoItems[id] = &TodoItem{
				ID:          id,
				Name:        m.editName,
				Description: m.editDesc,
			}
			appData.TodoOrder = append(appData.TodoOrder, id)
		} else {
			// Edit existing
			if todo, ok := appData.TodoItems[m.editingID]; ok {
				todo.Name = m.editName
				todo.Description = m.editDesc
			}
		}
		saveData()
		appData.mu.Unlock()
		m.mode = adminView
	case "backspace":
		if m.editField == 0 && len(m.editName) > 0 {
			m.editName = m.editName[:len(m.editName)-1]
		} else if m.editField == 1 && len(m.editDesc) > 0 {
			m.editDesc = m.editDesc[:len(m.editDesc)-1]
		}
	default:
		if len(msg.String()) == 1 {
			if m.editField == 0 && len(m.editName) < 50 {
				m.editName += msg.String()
			} else if m.editField == 1 && len(m.editDesc) < 200 {
				m.editDesc += msg.String()
			}
		}
	}
	return m, nil
}

func (m model) updateAdminProgressView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.mode = adminView
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) updateAdminDeleteConfirmView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		appData.mu.Lock()
		delete(appData.TodoItems, m.deleteConfirmID)
		newOrder := make([]string, 0, len(appData.TodoOrder)-1)
		for _, id := range appData.TodoOrder {
			if id != m.deleteConfirmID {
				newOrder = append(newOrder, id)
			}
		}
		appData.TodoOrder = newOrder
		saveData()
		appData.mu.Unlock()

		if m.cursor >= len(appData.TodoOrder) && m.cursor > 0 {
			m.cursor--
		}
		m.deleteConfirmID = ""
		m.mode = adminView
	case "n", "esc":
		m.deleteConfirmID = ""
		m.mode = adminView
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m model) View() string {
	switch m.mode {
	case usernameView:
		return m.viewUsername()
	case todoListView:
		return m.viewTodoList()
	case todoDetailView:
		return m.viewTodoDetail()
	case adminView:
		return m.viewAdmin()
	case adminEditView:
		return m.viewAdminEdit()
	case adminProgressView:
		return m.viewAdminProgress()
	case adminDeleteConfirmView:
		return m.viewAdminDeleteConfirm()
	}
	return ""
}

func (m model) viewUsername() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))

	s := titleStyle.Render("Welcome to Checklist App!") + "\n\n"
	s += "Please enter your username:\n\n"
	s += "> " + m.input + "\n\n"
	s += lipgloss.NewStyle().Faint(true).Render("Press Enter to continue, Esc to quit")
	return s
}

func (m model) viewTodoList() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	s := titleStyle.Render(fmt.Sprintf("Welcome, %s!", m.user.Username)) + "\n\n"
	s += "Todo Items:\n\n"

	appData.mu.RLock()
	defer appData.mu.RUnlock()

	if len(appData.TodoOrder) == 0 {
		s += "No todo items yet. Wait for admin to add some!\n"
	} else {
		for i, todoID := range appData.TodoOrder {
			todo := appData.TodoItems[todoID]
			cursor := " "
			if i == m.cursor {
				cursor = ">"
			}

			completed := ""
			if m.user.Completions[todoID] {
				completed = " " + greenStyle.Render("✓")
			}

			s += fmt.Sprintf("%s %d. %s%s\n", cursor, i+1, todo.Name, completed)
		}
	}

	s += "\n" + lipgloss.NewStyle().Faint(true).Render("↑/↓: navigate • Enter: view details • q: quit")
	return s
}

func (m model) viewTodoDetail() string {
	if m.selectedTodo == nil {
		return "Error: no todo selected"
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	s := titleStyle.Render(m.selectedTodo.Name) + "\n\n"
	s += m.selectedTodo.Description + "\n\n"

	appData.mu.RLock()
	completed := m.user.Completions[m.selectedTodo.ID]
	appData.mu.RUnlock()

	if completed {
		s += greenStyle.Render("✓ Completed") + "\n\n"
		s += lipgloss.NewStyle().Faint(true).Render("Esc: back")
	} else {
		s += lipgloss.NewStyle().Faint(true).Render("Enter: mark complete • Esc: back")
	}

	return s
}

func (m model) viewAdmin() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	s := titleStyle.Render(fmt.Sprintf("Admin Panel - %s", m.user.Username)) + "\n\n"
	s += "Todo Items:\n\n"

	appData.mu.RLock()
	defer appData.mu.RUnlock()

	if len(appData.TodoOrder) == 0 {
		s += "No todo items yet.\n"
	} else {
		// Count total non-admin users
		totalUsers := 0
		for _, user := range appData.Users {
			if !user.IsAdmin {
				totalUsers++
			}
		}

		for i, todoID := range appData.TodoOrder {
			todo := appData.TodoItems[todoID]
			cursor := " "
			if i == m.cursor {
				cursor = ">"
			}

			// Count completions for this todo
			completions := 0
			for _, user := range appData.Users {
				if !user.IsAdmin && user.Completions[todoID] {
					completions++
				}
			}

			progress := ""
			if totalUsers > 0 {
				progress = dimStyle.Render(fmt.Sprintf(" (%d/%d)", completions, totalUsers))
			}

			s += fmt.Sprintf("%s %d. %s%s\n", cursor, i+1, todo.Name, progress)
		}
	}

	s += "\n" + lipgloss.NewStyle().Faint(true).Render("↑/↓: navigate • Shift+J/K: move • a: add • e: edit • d: delete • p: progress • q: quit")
	return s
}

func (m model) viewAdminEdit() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))

	title := "Add New Todo"
	if m.editingID != "" {
		title = "Edit Todo"
	}

	s := titleStyle.Render(title) + "\n\n"

	nameLabel := "Name: "
	if m.editField == 0 {
		nameLabel = "> Name: "
	}
	s += nameLabel + m.editName + "\n\n"

	descLabel := "Description: "
	if m.editField == 1 {
		descLabel = "> Description: "
	}
	s += descLabel + m.editDesc + "\n\n"

	s += lipgloss.NewStyle().Faint(true).Render("Tab: switch field • Enter: save • Esc: cancel")
	return s
}

func (m model) viewAdminProgress() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	greenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	s := titleStyle.Render("Completion Progress") + "\n\n"

	appData.mu.RLock()
	defer appData.mu.RUnlock()

	if len(appData.TodoOrder) == 0 {
		s += "No todo items.\n"
		return s
	}

	// Header
	s += fmt.Sprintf("%-20s", "User")
	for _, todoID := range appData.TodoOrder {
		todo := appData.TodoItems[todoID]
		name := todo.Name
		if len(name) > 10 {
			name = name[:10]
		}
		s += fmt.Sprintf("%-12s", name)
	}
	s += "\n" + strings.Repeat("-", 20+12*len(appData.TodoOrder)) + "\n"

	// Rows
	for _, user := range appData.Users {
		if user.IsAdmin {
			continue
		}
		username := user.Username
		if len(username) > 20 {
			username = username[:20]
		}
		s += fmt.Sprintf("%-20s", username)

		for _, todoID := range appData.TodoOrder {
			if user.Completions[todoID] {
				s += greenStyle.Render(fmt.Sprintf("%-12s", "✓"))
			} else {
				s += fmt.Sprintf("%-12s", "-")
			}
		}
		s += "\n"
	}

	s += "\n" + lipgloss.NewStyle().Faint(true).Render("Esc: back")
	return s
}

// Data persistence
func saveData() {
	data, err := json.MarshalIndent(appData, "", "  ")
	if err != nil {
		log.Printf("Error marshaling data: %v", err)
		return
	}

	if err := os.WriteFile(dataFile, data, 0644); err != nil {
		log.Printf("Error writing data file: %v", err)
	}
}

func loadData() {
	data, err := os.ReadFile(dataFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Error reading data file: %v", err)
		}
		return
	}

	if err := json.Unmarshal(data, appData); err != nil {
		log.Printf("Error unmarshaling data: %v", err)
	}
}

// SSH handlers
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {
	publicKey := s.PublicKey().Marshal()
	pkStr := fmt.Sprintf("%x", publicKey)

	pty, _, active := s.Pty()
	if !active {
		wish.Fatalln(s, "no active terminal")
		return nil, nil
	}

	m := initialModel(pkStr)
	m.width = pty.Window.Width
	m.height = pty.Window.Height

	return m, []tea.ProgramOption{tea.WithAltScreen()}
}

func main() {
	loadData()

	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/term_info_ed25519"),
		wish.WithPublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
			return true // Accept all keys
		}),
		wish.WithMiddleware(
			bubbletea.Middleware(teaHandler),
			logging.Middleware(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("Starting SSH server on %s:%d", host, port)
	go func() {
		if err = s.ListenAndServe(); err != nil {
			log.Fatalln(err)
		}
	}()

	<-done
	log.Println("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalln(err)
	}
}
