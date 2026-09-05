package main

import "strings"

// Repair presentation of legacy response-item IDs using their recorded source,
// without rewriting usage events, counters, event IDs, or price history. A
// single source must identify an existing native task on the same device.
// Missing/ambiguous sources and explicit parent relationships are left intact.
const sessionParentsCTE = `legacy_sources AS (
	SELECT u.host_id,u.conversation_id,MIN(u.source_file_id) source_id
	FROM usage_events u JOIN sessions original ON original.host_id=u.host_id AND original.conversation_id=u.conversation_id
	WHERE COALESCE(original.parent_conversation_id,'')=''
	AND (u.conversation_id GLOB 'msg_*' OR u.conversation_id GLOB 'ctc_*' OR u.conversation_id GLOB 'ctco_*' OR u.conversation_id GLOB 'fco_*')
	GROUP BY u.host_id,u.conversation_id HAVING COUNT(DISTINCT u.source_file_id)=1
), resolved_sessions AS (
	SELECT s.*,COALESCE(NULLIF(s.parent_conversation_id,''),source.conversation_id,'') display_parent_id
	FROM sessions s
	LEFT JOIN legacy_sources l ON l.host_id=s.host_id AND l.conversation_id=s.conversation_id
	LEFT JOIN sessions source ON source.host_id=s.host_id AND source.conversation_id=l.source_id
	AND source.conversation_id<>s.conversation_id AND length(source.conversation_id)=36
	AND substr(source.conversation_id,9,1)='-' AND substr(source.conversation_id,14,1)='-'
	AND substr(source.conversation_id,19,1)='-' AND substr(source.conversation_id,24,1)='-'
	AND length(replace(source.conversation_id,'-',''))=32 AND source.conversation_id NOT GLOB '*[^0-9a-fA-F-]*'
)`

// Aliases are explicit administrator choices, not fuzzy title matching. They
// rename project groups only: two genuine tasks remain two tasks in one group.
func (s *server) displayProjectName(project, conversationName string) string {
	name := effectiveProjectName(project, conversationName)
	if canonical := cleanDisplayName(s.cfg.ProjectAliases[name], 100); canonical != "" {
		return canonical
	}
	return name
}

func legacyMessageID(id string) bool {
	return strings.HasPrefix(id, "msg_") || strings.HasPrefix(id, "ctc_")
}

func nativeTaskID(id string) bool {
	return len(id) == 36 && uuidInName.FindString(id) == id
}
