package fleetusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	dayLayout   = "2006-01-02"
	tempMarker  = ".tmp-"
	corruptMark = ".corrupt-"
)

var dayFilePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.json(\.corrupt-\d+)?$`)

type dayFile struct {
	Day     string       `json:"day"`
	Buckets []fileBucket `json:"buckets"`
}

type fileBucket struct {
	Hour     time.Time `json:"hour"`
	Key      string    `json:"key"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Counters
}

type daySnapshot struct {
	day     time.Time
	buckets []fileBucket
}

func dayFileName(day time.Time) string {
	return day.UTC().Format(dayLayout) + ".json"
}

func parseDayFileName(name string) (day time.Time, corrupt bool, ok bool) {
	match := dayFilePattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false, false
	}
	parsed, errParse := time.Parse(dayLayout, match[1])
	if errParse != nil {
		return time.Time{}, false, false
	}
	return parsed, match[2] != "", true
}

func isTempFileName(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, tempMarker)
}

func (l *Ledger) Load() {
	if l == nil || l.dir == "" {
		return
	}
	entries, errRead := os.ReadDir(l.dir)
	if errRead != nil {
		if !errors.Is(errRead, fs.ErrNotExist) {
			log.Warnf("fleet key usage: cannot list %s: %v", l.dir, errRead)
		}
		return
	}
	now := l.now()
	cutoff := retentionCutoff(now)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(l.dir, name)
		if isTempFileName(name) {
			removeQuietly(path)
			continue
		}
		day, corrupt, ok := parseDayFileName(name)
		if !ok {
			continue
		}
		if day.Before(cutoff) {
			removeQuietly(path)
			continue
		}
		if corrupt {
			continue
		}
		buckets, errDay := readDayFile(path, day)
		if errDay != nil {
			quarantine(path, now, errDay)
			continue
		}
		for _, bucket := range buckets {
			key := bucketKey{hour: bucket.Hour.Unix(), key: bucket.Key, provider: bucket.Provider, model: bucket.Model}
			l.add(day.Unix(), key, bucket.Counters, false)
		}
	}
}

func readDayFile(path string, day time.Time) ([]fileBucket, error) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	var file dayFile
	if errDecode := json.Unmarshal(data, &file); errDecode != nil {
		return nil, errDecode
	}
	if file.Day != day.Format(dayLayout) {
		return nil, fmt.Errorf("day %q does not match the file name", file.Day)
	}
	dayEnd := day.Add(24 * time.Hour)
	for _, bucket := range file.Buckets {
		hour := bucket.Hour.UTC()
		switch {
		case hour.Before(day) || !hour.Before(dayEnd) || !hour.Equal(hour.Truncate(time.Hour)):
			return nil, fmt.Errorf("bucket hour %s is outside the day", hour.Format(time.RFC3339))
		case bucket.Key == "" || bucket.Provider == "" || bucket.Model == "":
			return nil, errors.New("bucket without key, provider or model")
		case bucket.Counters.negative():
			return nil, errors.New("negative counter")
		}
	}
	return file.Buckets, nil
}

func quarantine(path string, now time.Time, cause error) {
	target := path + corruptMark + strconv.FormatInt(now.Unix(), 10)
	if errRename := os.Rename(path, target); errRename != nil {
		log.Warnf("fleet key usage: day file %s is unreadable (%v) and could not be moved aside: %v; the day starts empty", path, cause, errRename)
		return
	}
	log.Warnf("fleet key usage: day file %s is unreadable (%v); moved to %s, the day starts empty", path, cause, target)
}

func removeQuietly(path string) {
	if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
		log.Warnf("fleet key usage: cannot remove %s: %v", path, errRemove)
	}
}

func (l *Ledger) Flush() {
	if l == nil || l.dir == "" {
		return
	}
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	for _, snapshot := range l.takeDirty() {
		if errWrite := writeDayFile(l.dir, snapshot); errWrite != nil {
			log.Warnf("fleet key usage: cannot write %s: %v", dayFileName(snapshot.day), errWrite)
			l.markDirty(snapshot.day.Unix())
		}
	}
}

func (l *Ledger) takeDirty() []daySnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshots := make([]daySnapshot, 0, len(l.dirty))
	for day := range l.dirty {
		delete(l.dirty, day)
		buckets, exists := l.days[day]
		if !exists {
			continue
		}
		snapshot := daySnapshot{day: time.Unix(day, 0).UTC(), buckets: make([]fileBucket, 0, len(buckets))}
		for key, counters := range buckets {
			snapshot.buckets = append(snapshot.buckets, fileBucket{
				Hour:     time.Unix(key.hour, 0).UTC(),
				Key:      key.key,
				Provider: key.provider,
				Model:    key.model,
				Counters: *counters,
			})
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

func (l *Ledger) markDirty(day int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.days[day]; exists {
		l.dirty[day] = struct{}{}
	}
}

func writeDayFile(dir string, snapshot daySnapshot) (errWrite error) {
	sort.Slice(snapshot.buckets, func(i, j int) bool {
		a, b := snapshot.buckets[i], snapshot.buckets[j]
		switch {
		case !a.Hour.Equal(b.Hour):
			return a.Hour.Before(b.Hour)
		case a.Key != b.Key:
			return a.Key < b.Key
		case a.Provider != b.Provider:
			return a.Provider < b.Provider
		default:
			return a.Model < b.Model
		}
	})
	data, errMarshal := json.Marshal(dayFile{Day: snapshot.day.Format(dayLayout), Buckets: snapshot.buckets})
	if errMarshal != nil {
		return errMarshal
	}
	if errMkdir := ensureDir(dir); errMkdir != nil {
		return errMkdir
	}
	name := dayFileName(snapshot.day)
	temp, errCreate := os.CreateTemp(dir, "."+name+tempMarker+"*")
	if errCreate != nil {
		return errCreate
	}
	tempPath := temp.Name()
	defer func() {
		if errWrite != nil {
			_ = temp.Close()
			removeQuietly(tempPath)
		}
	}()
	if errChmod := temp.Chmod(0o600); errChmod != nil {
		return errChmod
	}
	if _, errData := temp.Write(data); errData != nil {
		return errData
	}
	if errSync := temp.Sync(); errSync != nil {
		return errSync
	}
	if errClose := temp.Close(); errClose != nil {
		return errClose
	}
	if errRename := os.Rename(tempPath, filepath.Join(dir, name)); errRename != nil {
		return errRename
	}
	syncDir(dir)
	return nil
}

func ensureDir(dir string) error {
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return errMkdir
	}
	return os.Chmod(dir, 0o700)
}

func syncDir(dir string) {
	handle, errOpen := os.Open(dir)
	if errOpen != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}

func (l *Ledger) Prune() {
	if l == nil {
		return
	}
	cutoff := retentionCutoff(l.now())
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.mu.Lock()
	for day := range l.days {
		if day < cutoff.Unix() {
			delete(l.days, day)
			delete(l.dirty, day)
		}
	}
	l.mu.Unlock()
	if l.dir == "" {
		return
	}
	entries, errRead := os.ReadDir(l.dir)
	if errRead != nil {
		if !errors.Is(errRead, fs.ErrNotExist) {
			log.Warnf("fleet key usage: cannot list %s: %v", l.dir, errRead)
		}
		return
	}
	for _, entry := range entries {
		if day, _, ok := parseDayFileName(entry.Name()); ok && !entry.IsDir() && day.Before(cutoff) {
			removeQuietly(filepath.Join(l.dir, entry.Name()))
		}
	}
}
