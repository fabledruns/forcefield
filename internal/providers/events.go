package providers

type StreamEvent struct {
    Text     string
    Thinking string
    Done     bool
    Err      error
}