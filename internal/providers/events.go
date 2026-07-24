package providers

type StreamEvent struct {
    Token string
    Done  bool
    Err   error
}