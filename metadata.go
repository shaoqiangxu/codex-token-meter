package main

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var genericProjectNames = map[string]bool{
	"": true, ".": true, "repo": true, "root": true, "workspace": true, "worktree": true,
}

var quotedProjectName = regexp.MustCompile(`[“"]([^“”"]{2,80})[”"]项目`)

// collectSessionMetadata reads display metadata only. Keep the selected
// columns explicit: title, first_user_message, preview, and conversation
// content must never be queried or uploaded. Agent paths are reduced to a
// basename locally before use as a subtask label.
func collectSessionMetadata(homes []string) []SessionMetadata {
	byID := map[string]SessionMetadata{}
	var order []string
	var databases []string
	for _, home := range homes {
		matches, _ := filepath.Glob(filepath.Join(home, "state_*.sqlite"))
		sort.Slice(matches, func(i, j int) bool {
			left, le := os.Stat(matches[i])
			right, re := os.Stat(matches[j])
			if le != nil || re != nil {
				return matches[i] > matches[j]
			}
			return left.ModTime().After(right.ModTime())
		})
		databases = append(databases, matches...)
	}
	for _, path := range databases {
		collectMetadataFromDB(path, byID, &order)
	}
	result := make([]SessionMetadata, 0, len(byID))
	for _, id := range order {
		item := byID[id]
		if item.ConversationName != "" || item.ProjectName != "" {
			result = append(result, item)
		}
	}
	return result
}

func collectMetadataFromDB(path string, byID map[string]SessionMetadata, order *[]string) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout%28500%29"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return
	}
	defer db.Close()
	cols := tableColumns(db, "threads")
	if !cols["id"] || !cols["name"] {
		return
	}
	cwdExpr, gitExpr, projectIDExpr, nicknameExpr, agentPathExpr, orderExpr := "''", "''", "''", "''", "''", "rowid"
	if cols["cwd"] {
		cwdExpr = "COALESCE(cwd,'')"
	}
	if cols["git_origin_url"] {
		gitExpr = "COALESCE(git_origin_url,'')"
	}
	if cols["project_id"] {
		projectIDExpr = "COALESCE(project_id,'')"
	}
	if cols["agent_nickname"] {
		nicknameExpr = "COALESCE(agent_nickname,'')"
	}
	if cols["agent_path"] {
		agentPathExpr = "COALESCE(agent_path,'')"
	}
	if cols["updated_at_ms"] {
		orderExpr = "updated_at_ms"
	} else if cols["updated_at"] {
		orderExpr = "updated_at"
	}
	query := "SELECT id,COALESCE(name,'')," + cwdExpr + "," + gitExpr + "," + projectIDExpr + "," + nicknameExpr + "," + agentPathExpr + " FROM threads ORDER BY " + orderExpr + " DESC LIMIT 512"
	rows, err := db.Query(query)
	if err != nil {
		return
	}
	defer rows.Close()
	projects := readProjectNames(db)
	for rows.Next() {
		var id, name, cwd, remote, projectID, nickname, agentPath string
		if rows.Scan(&id, &name, &cwd, &remote, &projectID, &nickname, &agentPath) != nil || id == "" {
			continue
		}
		name = cleanDisplayName(name, 100)
		if name == "" {
			name = agentDisplayName(agentPath, nickname)
		}
		project := cleanDisplayName(projects[projectID], 100)
		if project == "" {
			project = projectNameFromRemote(remote)
		}
		if project == "" {
			project = projectNameFromPath(cwd)
		}
		if project == "" {
			project = effectiveProjectName("", name)
			if project == "未归属项目" {
				project = ""
			}
		}
		if _, exists := byID[id]; !exists {
			*order = append(*order, id)
			byID[id] = SessionMetadata{ConversationID: id, ConversationName: name, ProjectName: project}
		}
	}
}

func tableColumns(db *sql.DB, table string) map[string]bool {
	result := map[string]bool{}
	if !regexp.MustCompile(`^[a-z_]+$`).MatchString(table) {
		return result
	}
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk) == nil {
			result[name] = true
		}
	}
	return result
}

func readProjectNames(db *sql.DB) map[string]string {
	result := map[string]string{}
	cols := tableColumns(db, "projects")
	if !cols["id"] || !cols["name"] {
		return result
	}
	rows, err := db.Query("SELECT id,name FROM projects")
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			result[id] = name
		}
	}
	return result
}

func cleanDisplayName(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func projectNameFromRemote(remote string) string {
	remote = strings.TrimSpace(strings.TrimSuffix(remote, "/"))
	if remote == "" {
		return ""
	}
	if parsed, err := url.Parse(remote); err == nil && parsed.Path != "" {
		remote = parsed.Path
	} else if at := strings.LastIndex(remote, ":"); at >= 0 {
		remote = remote[at+1:]
	}
	name := filepath.Base(filepath.ToSlash(remote))
	name = strings.TrimSuffix(name, ".git")
	return cleanDisplayName(name, 100)
}

func projectNameFromPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	name := filepath.Base(filepath.Clean(path))
	parent := strings.ToLower(filepath.Base(filepath.Dir(filepath.Clean(path))))
	if parent == "users" || parent == "home" {
		return ""
	}
	if genericProjectNames[strings.ToLower(name)] || regexp.MustCompile(`^\d+$`).MatchString(name) {
		return ""
	}
	return cleanDisplayName(name, 100)
}

func agentDisplayName(agentPath, nickname string) string {
	name := projectNameFromPath(agentPath)
	name = strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), "-", " ")
	name = cleanDisplayName(name, 80)
	nickname = cleanDisplayName(nickname, 40)
	if name != "" && nickname != "" && !strings.EqualFold(name, nickname) {
		return name + " · " + nickname
	}
	if name != "" {
		return name
	}
	return nickname
}

func effectiveProjectName(project, conversationName string) string {
	project = cleanDisplayName(project, 100)
	if !genericProjectNames[strings.ToLower(project)] && !regexp.MustCompile(`^\d+$`).MatchString(project) {
		return project
	}
	conversationName = cleanDisplayName(conversationName, 100)
	if match := quotedProjectName.FindStringSubmatch(conversationName); len(match) == 2 {
		return match[1]
	}
	if conversationName != "" && conversationName != project {
		return conversationName
	}
	return "未归属项目"
}
