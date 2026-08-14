package mcp

func (s *Server) SessionCount() int {
	s.mu.Lock()
	expired := s.cleanupExpiredLocked(s.now())
	count := len(s.sessions)
	s.mu.Unlock()
	closeSessions(expired)
	return count
}

func (s *Server) PendingCount() int {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	count := 0
	for _, sess := range sessions {
		sess.mu.Lock()
		count += len(sess.inflight)
		sess.mu.Unlock()
	}
	return count
}
