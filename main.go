package main

import (
	"archive/zip"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed public/index.html
var webAssets embed.FS

const defaultMaxUploadBytes int64 = 10 * 1024 * 1024 * 1024

var version = "dev"
var commit = "unknown"
var buildTime = "unknown"

type app struct {
	root           *os.Root
	storageRoot    string
	listenHost     string
	port           string
	maxUploadBytes int64
	authUser       string
	authPassword   string
}

type directoryItem struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Type  string    `json:"type"`
	Size  *int64    `json:"size"`
	Mtime time.Time `json:"mtime"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("lan-file-server %s (%s, %s)\n", version, commit, buildTime)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	storageRoot := envOr("STORAGE_ROOT", "./storage")
	absoluteRoot, err := pathAbs(storageRoot)
	if err != nil {
		return fmt.Errorf("解析存储目录失败: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0750); err != nil {
		return fmt.Errorf("创建存储目录失败: %w", err)
	}
	root, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return fmt.Errorf("打开存储目录失败: %w", err)
	}
	defer root.Close()

	user := os.Getenv("AUTH_USER")
	password := os.Getenv("AUTH_PASSWORD")
	if user == "" || password == "" {
		return errors.New("必须设置 AUTH_USER 和 AUTH_PASSWORD，禁止无认证开放文件管理接口")
	}
	maxUpload := defaultMaxUploadBytes
	if raw := os.Getenv("MAX_UPLOAD_BYTES"); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed <= 0 {
			return errors.New("MAX_UPLOAD_BYTES 必须是正整数")
		}
		maxUpload = parsed
	}

	application := &app{
		root: root, storageRoot: absoluteRoot,
		listenHost: envOr("HOST", "0.0.0.0"), port: envOr("PORT", "3000"),
		maxUploadBytes: maxUpload, authUser: user, authPassword: password,
	}
	server := &http.Server{
		Addr:              net.JoinHostPort(application.listenHost, application.port),
		Handler:           application.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	fmt.Printf("LAN 文件服务器监听 %s，存储目录 %s\n", server.Addr, absoluteRoot)
	return server.ListenAndServe()
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	assets, _ := fs.Sub(webAssets, "public")
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.Handle("/api/", a.requireAuth(http.HandlerFunc(a.handleAPI)))
	return mux
}

func (a *app) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.authUser)) == 1
		passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(a.authPassword)) == 1
		if !ok || !userOK || !passwordOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="LAN 文件服务器", charset="UTF-8"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "需要登录。"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/config":
		writeJSON(w, http.StatusOK, map[string]any{
			"storageRoot": a.storageRoot, "listenHost": a.listenHost,
			"port": a.port, "maxUploadBytes": a.maxUploadBytes,
			"urls": a.accessURLs(),
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/list":
		a.listDirectory(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
		a.uploadFile(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/download-file":
		a.downloadFile(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/download-folder":
		a.downloadFolder(w, r)
	case r.Method == http.MethodDelete && r.URL.Path == "/api/item":
		a.deleteItem(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "接口不存在。"})
	}
}

func (a *app) listDirectory(w http.ResponseWriter, r *http.Request) {
	relative, err := cleanRelative(r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, err)
		return
	}
	if err = a.root.MkdirAll(relative, 0750); err != nil {
		apiError(w, err)
		return
	}
	dir, err := a.root.Open(relative)
	if err != nil {
		apiError(w, err)
		return
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目标不是文件夹。"})
		return
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		apiError(w, err)
		return
	}
	items := make([]directoryItem, 0, len(entries))
	for _, entry := range entries {
		// long: 忽略符号链接，避免 UI 将链接目标误显示为存储区内可管理文件。
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := path.Join(relative, entry.Name())
		info, statErr := a.root.Stat(child)
		if statErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		kind := "file"
		var size *int64
		if info.IsDir() {
			kind = "directory"
		} else {
			fileSize := info.Size()
			size = &fileSize
		}
		items = append(items, directoryItem{Name: entry.Name(), Path: child, Type: kind, Size: size, Mtime: info.ModTime()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"currentPath": relative, "items": items})
}

func (a *app) uploadFile(w http.ResponseWriter, r *http.Request) {
	base, err := cleanRelative(r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, err)
		return
	}
	name, err := cleanRelative(r.URL.Query().Get("relative"))
	if err != nil || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 relative 文件路径参数。"})
		return
	}
	if r.ContentLength > a.maxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "文件超过上传大小限制。"})
		return
	}
	target := path.Join(base, name)
	parent := path.Dir(target)
	if err := a.root.MkdirAll(parent, 0750); err != nil {
		apiError(w, err)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, a.maxUploadBytes)
	defer limited.Close()
	tempPath := path.Join(parent, fmt.Sprintf(".%s.uploading-%d", path.Base(target), time.Now().UnixNano()))
	tmp, err := a.root.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		apiError(w, err)
		return
	}
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = a.root.Remove(tempPath)
		}
	}()
	written, err := io.Copy(tmp, limited)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "文件超过上传大小限制。"})
			return
		}
		apiError(w, err)
		return
	}
	if written == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有收到文件内容。"})
		return
	}
	if err := tmp.Sync(); err != nil {
		apiError(w, err)
		return
	}
	if err := tmp.Close(); err != nil {
		apiError(w, err)
		return
	}
	if err := a.root.Rename(tempPath, target); err != nil {
		apiError(w, err)
		return
	}
	removeTemp = false
	writeJSON(w, http.StatusOK, map[string]any{"message": "上传成功。", "path": target, "size": written})
}

func (a *app) downloadFile(w http.ResponseWriter, r *http.Request) {
	relative, err := requiredRelative(r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, err)
		return
	}
	if err := a.rejectFinalSymlink(relative); err != nil {
		apiError(w, err)
		return
	}
	file, err := a.root.Open(relative)
	if err != nil {
		apiError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目标不是文件。"})
		return
	}
	serveAttachment(w, r, file, path.Base(relative), info.ModTime())
}

func (a *app) downloadFolder(w http.ResponseWriter, r *http.Request) {
	relative, err := cleanRelative(r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, err)
		return
	}
	if relative != "" {
		if err := a.rejectFinalSymlink(relative); err != nil {
			apiError(w, err)
			return
		}
	}
	dir, err := a.root.Open(relative)
	if err != nil {
		apiError(w, err)
		return
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "目标不是文件夹。"})
		return
	}
	folderName := path.Base(relative)
	if relative == "" {
		folderName = "root"
	}
	w.Header().Set("Content-Type", "application/zip")
	setAttachmentHeader(w, folderName+".zip")
	zipWriter := zip.NewWriter(w)
	err = a.addDirectoryToZip(zipWriter, relative, folderName)
	closeErr := zipWriter.Close()
	if err != nil || closeErr != nil {
		return
	}
}

func (a *app) addDirectoryToZip(archive *zip.Writer, relative, archiveName string) error {
	dir, err := a.root.Open(relative)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		// long: 不跟随链接打包，防止 ZIP 越过存储根目录读取主机其他文件。
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := path.Join(relative, entry.Name())
		zipPath := path.Join(archiveName, entry.Name())
		info, err := a.root.Stat(child)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if _, err := archive.Create(zipPath + "/"); err != nil {
				return err
			}
			if err := a.addDirectoryToZip(archive, child, zipPath); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out, err := archive.Create(zipPath)
		if err != nil {
			return err
		}
		in, err := a.root.Open(child)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (a *app) deleteItem(w http.ResponseWriter, r *http.Request) {
	relative, err := requiredRelative(r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, err)
		return
	}
	if err := a.rejectFinalSymlink(relative); err != nil {
		apiError(w, err)
		return
	}
	info, err := a.root.Stat(relative)
	if err != nil {
		apiError(w, err)
		return
	}
	if info.IsDir() {
		err = a.root.RemoveAll(relative)
	} else {
		err = a.root.Remove(relative)
	}
	if err != nil {
		apiError(w, err)
		return
	}
	message := "文件删除成功。"
	if info.IsDir() {
		message = "文件夹删除成功。"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": message, "path": relative})
}

func (a *app) rejectFinalSymlink(relative string) error {
	info, err := a.root.Lstat(relative)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("不支持直接操作符号链接。")
	}
	return nil
}

func (a *app) accessURLs() []string {
	port := a.port
	if a.listenHost != "0.0.0.0" && a.listenHost != "::" {
		return []string{"http://" + net.JoinHostPort(a.listenHost, port)}
	}
	urls := []string{"http://localhost:" + port}
	interfaces, err := net.Interfaces()
	if err != nil {
		return urls
	}
	seen := map[string]bool{}
	for _, iface := range interfaces {
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() && !seen[ip.String()] {
				seen[ip.String()] = true
				urls = append(urls, "http://"+net.JoinHostPort(ip.String(), port))
			}
		}
	}
	return urls
}

func cleanRelative(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	value = strings.TrimLeft(value, "/")
	if value == "" || value == "." {
		return "", nil
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("路径不允许包含 ..")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", errors.New("非法路径。")
	}
	return cleaned, nil
}

func requiredRelative(value string) (string, error) {
	cleaned, err := cleanRelative(value)
	if err != nil {
		return "", err
	}
	if cleaned == "" {
		return "", errors.New("不允许对存储根目录执行此操作。")
	}
	return cleaned, nil
}

func apiError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	message := "文件操作失败。"
	if errors.Is(err, fs.ErrNotExist) {
		status = http.StatusNotFound
		message = "目标不存在。"
	} else if errors.Is(err, fs.ErrPermission) {
		status = http.StatusForbidden
		message = "没有权限访问目标。"
	} else {
		switch err.Error() {
		case "路径不允许包含 ..", "非法路径。", "不允许对存储根目录执行此操作。", "不支持直接操作符号链接。":
			message = err.Error()
		}
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func serveAttachment(w http.ResponseWriter, r *http.Request, file *os.File, name string, modified time.Time) {
	setAttachmentHeader(w, name)
	http.ServeContent(w, r, name, modified, file)
}

func setAttachmentHeader(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func pathAbs(value string) (string, error) {
	return filepath.Abs(value)
}
