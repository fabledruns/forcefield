package runtime

type EventType int

const (
    EventToken EventType = iota
    EventToolStart
    EventToolEnd
    EventDone
    EventError
)

type Event struct {
    Type EventType
    Text string
    Tool string
    Err  error
}