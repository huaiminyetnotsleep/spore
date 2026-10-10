package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// RecoveryCandidateLimit 限制快照和关联历史的内存；超限明确拒绝，不截断历史。
const RecoveryCandidateLimit = 50000

// NormalizeRecoveryKey 统一@用户名和-100频道ID；不从运行配置猜来源。
func NormalizeRecoveryKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	v = strings.TrimPrefix(v, "@")
	if strings.HasPrefix(v, "-100") {
		v = strings.TrimPrefix(v, "-100")
	}
	if n, e := strconv.ParseInt(v, 10, 64); e == nil {
		if n <= 0 {
			return "", apperr.New(apperr.CodeInvalidURL, "频道ID无效")
		}
		return strconv.FormatInt(n, 10), nil
	}
	r, ok := tmeurl.Parse("https://t.me/" + v + "/1")
	if !ok || r.Kind != tmeurl.PeerUsername {
		return "", apperr.New(apperr.CodeInvalidURL, "频道用户名无效")
	}
	return strings.ToLower(r.Username), nil
}

type recoveryHistory struct {
	key      string
	message  int
	members  []int
	copy     RecoveryCopy
	selected bool
	unknown  bool
}

// RecoveryCandidates 汇集三个历史集合，关联全范围相册元数据后构建稳定快照。
func (s *Store) RecoveryCandidates(ctx context.Context, f RecoveryFilter) ([]RecoveryItem, []string, error) {
	key, err := NormalizeRecoveryKey(f.ChannelKey)
	if err != nil {
		return nil, nil, err
	}
	f.ChannelKey = key
	if f.UserID < 0 || f.Since < 0 || f.Until < 0 || (f.Until != 0 && f.Since >= f.Until) {
		return nil, nil, apperr.New(apperr.CodeInvalidURL, "历史范围无效")
	}
	// A username observed against multiple IDs is ambiguous: do not invent an alias.
	aliasIDs := map[string]map[string]bool{}
	rows, err := s.ex.QueryContext(ctx, `SELECT DISTINCT lower(username),channel_id FROM watch_events WHERE username!='' UNION SELECT lower(COALESCE(username,'')),channel_id FROM watch_sources WHERE COALESCE(username,'')!='' LIMIT ?`, RecoveryCandidateLimit+1)
	if err != nil {
		return nil, nil, wrapDB("读取历史来源别名", err)
	}
	count := 0
	for rows.Next() {
		var name string
		var id int64
		if err = rows.Scan(&name, &id); err != nil {
			rows.Close()
			return nil, nil, wrapDB("扫描来源别名", err)
		}
		count++
		if count > RecoveryCandidateLimit {
			rows.Close()
			return nil, nil, recoveryTooLarge()
		}
		canon, e := NormalizeRecoveryKey(strconv.FormatInt(id, 10))
		if e != nil {
			continue
		}
		if aliasIDs[name] == nil {
			aliasIDs[name] = map[string]bool{}
		}
		aliasIDs[name][canon] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, wrapDB("遍历来源别名", err)
	}
	aliases := map[string]string{}
	for name, ids := range aliasIDs {
		if len(ids) == 1 {
			for id := range ids {
				aliases[name] = id
			}
		}
	}
	canonical := func(v string) string {
		k, e := NormalizeRecoveryKey(v)
		if e != nil {
			return ""
		}
		if c := aliases[k]; c != "" {
			return c
		}
		return k
	}
	if c := aliases[key]; c != "" {
		key = c
	}
	// Phase one chooses scope over all historical rows, not the current UI page.
	query := `SELECT channel_key,message_id,'[]','[]',0,0 FROM requests WHERE (?=0 OR user_id=?) AND (?=0 OR requested_at>=?) AND (?=0 OR requested_at<?)
 UNION ALL SELECT d.channel_key,d.message_id,'[]',d.dump_ids_json,d.dump_channel_id,0 FROM dump_entries d WHERE (?=0 OR d.created_at>=?) AND (?=0 OR d.created_at<?) AND (?=0 OR EXISTS(SELECT 1 FROM requests r WHERE r.channel_key=d.channel_key AND r.message_id=d.message_id AND r.user_id=?) OR EXISTS(SELECT 1 FROM watch_sources w WHERE w.added_by=? AND (lower(w.username)=lower(d.channel_key) OR CAST(w.channel_id AS TEXT)=d.channel_key)))
 UNION ALL SELECT CAST(w.channel_id AS TEXT),w.message_id,w.member_ids_json,'[]',0,CASE WHEN w.dump_ids_json!='[]' THEN 1 ELSE 0 END FROM watch_events w LEFT JOIN watch_sources s ON s.channel_id=w.channel_id WHERE (?=0 OR w.created_at>=?) AND (?=0 OR w.created_at<?) AND (?=0 OR s.added_by=? OR EXISTS(SELECT 1 FROM requests r WHERE r.id=w.request_id AND r.user_id=?)) LIMIT ?`
	args := []any{f.UserID, f.UserID, f.Since, f.Since, f.Until, f.Until, f.Since, f.Since, f.Until, f.Until, f.UserID, f.UserID, f.UserID, f.Since, f.Since, f.Until, f.Until, f.UserID, f.UserID, f.UserID}
	query = strings.TrimSuffix(query, " LIMIT ?")
	if key != "" {
		filterKeys := []string{key}
		if _, e := strconv.ParseInt(key, 10, 64); e == nil {
			filterKeys = append(filterKeys, "-100"+key)
		}
		for name, id := range aliases {
			if id == key {
				filterKeys = append(filterKeys, name, "@"+name)
			}
		}
		query = "SELECT * FROM (" + query + ") WHERE lower(channel_key) IN (" + strings.TrimSuffix(strings.Repeat("?,", len(filterKeys)), ",") + ")"
		for _, k := range filterKeys {
			args = append(args, k)
		}
	}
	query += " LIMIT ?"
	args = append(args, RecoveryCandidateLimit+1)
	seeds, err := s.readRecoveryHistory(ctx, query, args, canonical)
	if err != nil {
		return nil, nil, err
	}
	selected := map[string]bool{}
	sourceKeys := map[string]bool{}
	for _, r := range seeds {
		if key != "" && r.key != key {
			continue
		}
		for _, id := range r.members {
			selected[fmt.Sprintf("%s:%d", r.key, id)] = true
		}
		sourceKeys[r.key] = true
	}
	if len(sourceKeys) == 0 {
		return []RecoveryItem{}, []string{}, nil
	}
	// Read associated source metadata beyond the selected date/user rows. This prevents
	// a date/page boundary from splitting a known album or losing its cache ownership.
	rawKeys := map[string]bool{}
	for k := range sourceKeys {
		rawKeys[k] = true
		if _, e := strconv.ParseInt(k, 10, 64); e == nil {
			rawKeys["-100"+k] = true
		}
		for name, id := range aliases {
			if id == k {
				rawKeys[name] = true
				rawKeys["@"+name] = true
			}
		}
	}
	keys := make([]string, 0, len(rawKeys))
	for k := range rawKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 9000 {
		return nil, nil, recoveryTooLarge()
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
	baseArgs := []any{}
	for _, k := range keys {
		baseArgs = append(baseArgs, k)
	}
	query = `SELECT channel_key,message_id,'[]','[]',0,0 FROM requests WHERE lower(channel_key) IN (` + placeholders + `) UNION ALL SELECT channel_key,message_id,'[]',dump_ids_json,dump_channel_id,0 FROM dump_entries WHERE lower(channel_key) IN (` + placeholders + `) UNION ALL SELECT CAST(channel_id AS TEXT),message_id,member_ids_json,'[]',0,CASE WHEN dump_ids_json!='[]' THEN 1 ELSE 0 END FROM watch_events WHERE CAST(channel_id AS TEXT) IN (` + placeholders + `) LIMIT ?`
	args = append(append(append([]any{}, baseArgs...), baseArgs...), baseArgs...)
	args = append(args, RecoveryCandidateLimit+1)
	history, err := s.readRecoveryHistory(ctx, query, args, canonical)
	if err != nil {
		return nil, nil, err
	}
	// Union by known source members and intersecting authoritative cache coordinates.
	parent := make([]int, len(history))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		a = find(a)
		b = find(b)
		if a != b {
			parent[b] = a
		}
	}
	members := map[string]int{}
	copies := map[string]int{}
	for i, r := range history {
		for _, id := range r.members {
			k := fmt.Sprintf("%s:%d", r.key, id)
			if prev, ok := members[k]; ok {
				union(i, prev)
			} else {
				members[k] = i
			}
		}
		if r.copy.ChatID != 0 {
			for _, id := range r.copy.MessageIDs {
				k := fmt.Sprintf("%d:%d", r.copy.ChatID, id)
				if prev, ok := copies[k]; ok {
					union(i, prev)
				} else {
					copies[k] = i
				}
			}
		}
	}
	groups := map[int][]recoveryHistory{}
	for i, r := range history {
		groups[find(i)] = append(groups[find(i)], r)
	}
	out := []RecoveryItem{}
	warnings := []string{}
	unknown := false
	singleton := false
	for _, group := range groups {
		ids := map[int]bool{}
		chosen := false
		cache := map[string]RecoveryCopy{}
		canon := group[0].key
		for _, r := range group {
			if r.key < canon {
				canon = r.key
			}
			for _, id := range r.members {
				ids[id] = true
				if selected[fmt.Sprintf("%s:%d", r.key, id)] {
					chosen = true
				}
			}
			if r.unknown {
				unknown = true
			}
			if r.copy.ChatID != 0 && len(r.copy.MessageIDs) > 0 {
				cache[recoveryJSON(r.copy)] = r.copy
			}
		}
		if !chosen {
			continue
		}
		ms := []int{}
		for id := range ids {
			ms = append(ms, id)
		}
		sort.Ints(ms)
		if len(ms) == 0 {
			continue
		}
		if len(ms) == 1 {
			singleton = true
		}
		cc := []RecoveryCopy{}
		for _, c := range cache { // Drop a coordinate subset when a complete superset in the same cache is known.
			subset := false
			for _, other := range cache {
				if other.ChatID == c.ChatID && len(other.MessageIDs) > len(c.MessageIDs) {
					set := map[int]bool{}
					for _, v := range other.MessageIDs {
						set[v] = true
					}
					all := true
					for _, v := range c.MessageIDs {
						all = all && set[v]
					}
					if all {
						subset = true
						break
					}
				}
			}
			if !subset {
				// Output count does not prove source coverage: one source member may
				// split into several outputs. Every known member must have an
				// authoritative dump-entry mapping contained in this copy set.
				set := map[int]bool{}
				for _, id := range c.MessageIDs {
					set[id] = true
				}
				covered := map[int]bool{}
				for _, r := range group {
					if r.copy.ChatID != c.ChatID || len(r.copy.MessageIDs) == 0 {
						continue
					}
					contained := true
					for _, id := range r.copy.MessageIDs {
						contained = contained && set[id]
					}
					if contained {
						for _, id := range r.members {
							covered[id] = true
						}
					}
				}
				complete := true
				for _, id := range ms {
					complete = complete && covered[id]
				}
				if len(ms) == 1 && len(c.MessageIDs) != 1 {
					complete = false
				} // unknown album vs split: never infer
				if complete {
					cc = append(cc, c)
				} else {
					warnings = append(warnings, "部分缓存副本无法证明覆盖已知全部源成员，将使用原来源。")
				}
			}
		}
		sort.Slice(cc, func(i, j int) bool {
			if cc[i].ChatID != cc[j].ChatID {
				return cc[i].ChatID < cc[j].ChatID
			}
			return recoveryJSON(cc[i].MessageIDs) < recoveryJSON(cc[j].MessageIDs)
		})
		out = append(out, RecoveryItem{ChannelKey: canon, MessageID: ms[0], MemberIDs: ms, CacheCopies: cc, SentIDs: []int{}, IdentityKey: recoveryIdentity(canon, ms[0])})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChannelKey != out[j].ChannelKey {
			return out[i].ChannelKey < out[j].ChannelKey
		}
		return out[i].MessageID < out[j].MessageID
	})
	if unknown {
		warnings = append(warnings, "部分历史缓存频道归属未知，不使用当前配置猜测；将尝试原来源。")
	}
	if singleton {
		warnings = append(warnings, "部分历史记录无相册成员信息，只恢复已确认的消息，不推断整个相册。")
	}
	warnings = append(warnings, "按来源和消息ID稳定排序，不代表跨来源全局时间顺序。")
	if err := s.addRecoveryOutputs(ctx, out); err != nil {
		return nil, nil, err
	}
	return out, warnings, nil
}

// Successful outputs can be another read-only recovery cache, but are never
// published into ordinary dump_entries implicitly.
func (s *Store) addRecoveryOutputs(ctx context.Context, items []RecoveryItem) error {
	coordinates := 0
	for idx := range items {
		rows, err := s.ex.QueryContext(ctx, `SELECT j.target_chat_id,i.sent_ids_json FROM recovery_items i JOIN recovery_jobs j ON j.id=i.job_id WHERE i.identity_key LIKE ? ESCAPE '\' AND i.status='succeeded' ORDER BY i.id DESC LIMIT 101`, recoveryIdentityPrefix(items[idx].ChannelKey))
		if err != nil {
			return wrapDB("读取已恢复副本", err)
		}
		seen := map[string]bool{}
		for _, c := range items[idx].CacheCopies {
			seen[recoveryJSON(c)] = true
		}
		n := 0
		for rows.Next() {
			n++
			var c RecoveryCopy
			var ids string
			if err = rows.Scan(&c.ChatID, &ids); err != nil {
				rows.Close()
				return wrapDB("扫描已恢复副本", err)
			}
			if n > 100 {
				rows.Close()
				return recoveryTooLarge()
			}
			if err = json.Unmarshal([]byte(ids), &c.MessageIDs); err != nil {
				rows.Close()
				return wrapDB("解析已恢复副本", err)
			}
			c.MessageIDs = positiveRecoveryIDs(c.MessageIDs)
			if len(c.MessageIDs) < len(items[idx].MemberIDs) {
				continue
			}
			coordinates += len(c.MessageIDs)
			if coordinates > RecoveryCandidateLimit*10 {
				rows.Close()
				return recoveryTooLarge()
			}
			k := recoveryJSON(c)
			if !seen[k] {
				items[idx].CacheCopies = append(items[idx].CacheCopies, c)
				seen[k] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return wrapDB("遍历已恢复副本", err)
		}
	}
	return nil
}
func recoveryTooLarge() error {
	return apperr.New(apperr.CodeStoreConstraint, "历史范围或关联来源过大，请缩小范围；未截断候选。")
}
func (s *Store) readRecoveryHistory(ctx context.Context, q string, args []any, canonical func(string) string) ([]recoveryHistory, error) {
	rows, err := s.ex.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapDB("读取恢复历史", err)
	}
	defer rows.Close()
	out := []recoveryHistory{}
	for rows.Next() {
		var r recoveryHistory
		var ms, cs string
		var unknown int
		if err = rows.Scan(&r.key, &r.message, &ms, &cs, &r.copy.ChatID, &unknown); err != nil {
			return nil, wrapDB("扫描恢复历史", err)
		}
		if len(out) >= RecoveryCandidateLimit {
			return nil, recoveryTooLarge()
		}
		r.key = canonical(r.key)
		if r.key == "" || r.message <= 0 {
			continue
		}
		if err = json.Unmarshal([]byte(ms), &r.members); err != nil {
			return nil, wrapDB("解析历史相册成员", err)
		}
		if err = json.Unmarshal([]byte(cs), &r.copy.MessageIDs); err != nil {
			return nil, wrapDB("解析历史缓存坐标", err)
		}
		r.members = positiveRecoveryIDs(membersOr(r.members, r.message))
		r.copy.MessageIDs = positiveRecoveryIDs(r.copy.MessageIDs)
		r.unknown = unknown != 0 || (r.copy.ChatID == 0 && len(r.copy.MessageIDs) > 0)
		out = append(out, r)
	}
	return out, wrapDB("遍历恢复历史", rows.Err())
}
func positiveRecoveryIDs(ids []int) []int {
	out := []int{}
	set := map[int]bool{}
	for _, id := range ids {
		if id > 0 && !set[id] {
			out = append(out, id)
			set[id] = true
		}
	}
	sort.Ints(out)
	return out
}
