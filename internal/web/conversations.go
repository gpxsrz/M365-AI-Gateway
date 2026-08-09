package web

import (
	"encoding/json"
	"net/http"
)

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "不支援此 HTTP 方法", http.StatusMethodNotAllowed)
		return
	}
	if s.checkpoints == nil {
		jsonOut(w, map[string]any{"conversations": []transportCheckpointView{}})
		return
	}
	conversations, err := s.checkpoints.List()
	if err != nil {
		http.Error(w, "無法讀取對話", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]any{"conversations": conversations})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "不支援此 HTTP 方法", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID == "" {
		http.Error(w, "JSON 格式錯誤", http.StatusBadRequest)
		return
	}
	if s.checkpoints == nil {
		http.Error(w, "找不到對話", http.StatusNotFound)
		return
	}
	deleted, err := s.checkpoints.Delete(body.ID)
	if err != nil {
		http.Error(w, "無法刪除對話", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "找不到對話", http.StatusNotFound)
		return
	}
	jsonOut(w, map[string]string{"status": "deleted"})
}
