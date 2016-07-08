package fam100

import (
	"strconv"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/rcrowley/go-metrics"
	"github.com/uber-go/zap"
)

var (
	RoundDuration        = 90 * time.Second
	tickDuration         = 10 * time.Second
	DelayBetweenRound    = 5 * time.Second
	TickAfterWrongAnswer = false
	RoundPerGame         = 3
	DefaultQuestionLimit = 450
	log                  zap.Logger

	gameMsgProcessTimer = metrics.NewRegisteredTimer("game.processedMessage", metrics.DefaultRegistry)
	playerActive        = metrics.NewRegisteredGauge("player.active", metrics.DefaultRegistry)
	playerActiveMap     = cache.New(5*time.Minute, 30*time.Second)
)

func init() {
	log = zap.NewJSON()
	go func() {
		for range time.Tick(30 * time.Second) {
			playerActive.Update(int64(playerActiveMap.ItemCount()))
		}
	}()
}

func SetLogger(l zap.Logger) {
	log = l.With(zap.String("module", "fam100"))
}

// Message to communicate between player and the game
type Message interface{}

// TextMessage represents a chat message
type TextMessage struct {
	ChanID string
	Player Player
	Text   string
}

// StateMessage represents state change in the game
type StateMessage struct {
	ChanID    string
	Round     int
	State     State
	RoundText QNAMessage //question and answer
}

// TickMessage represents time left notification
type TickMessage struct {
	ChanID   string
	TimeLeft time.Duration
}

type WrongAnswerMessage TickMessage

// QNAMessage represents question and answer for a round
type QNAMessage struct {
	ChanID         string
	Round          int
	QuestionText   string
	QuestionID     int
	Answers        []roundAnswers
	ShowUnanswered bool // reveal un-answered question (end of round)
	TimeLeft       time.Duration
}

type roundAnswers struct {
	Text       string
	Score      int
	Answered   bool
	PlayerName string
	Highlight  bool
}
type RankMessage struct {
	ChanID string
	Round  int
	Rank   Rank
	Final  bool
}

// PlayerID is the player ID type
type PlayerID string

// Player of the game
type Player struct {
	ID   PlayerID
	Name string
}

// State represents state of the round
type State string

// Available state
const (
	Created       State = "created"
	Started       State = "started"
	Finished      State = "finished"
	RoundStarted  State = "roundStarted"
	RoundTimeout  State = "RoundTimeout"
	RoundFinished State = "roundFinished"
)

// Game can consists of multiple round
// each round user will be asked question and gain points
type Game struct {
	ChanID           string
	ChanName         string
	State            State
	TotalRoundPlayed int
	players          map[PlayerID]Player
	seed             int64
	rank             Rank
	currentRound     *round

	In  chan Message
	Out chan Message
}

// NewGame create a new round
func NewGame(chanID, chanName string, in, out chan Message) (r *Game, err error) {
	seed, totalRoundPlayed, err := DefaultDB.nextGame(chanID)
	if err != nil {
		return nil, err
	}

	return &Game{
		ChanID:           chanID,
		ChanName:         chanName,
		State:            Created,
		players:          make(map[PlayerID]Player),
		seed:             seed,
		TotalRoundPlayed: totalRoundPlayed,
		In:               in,
		Out:              out,
	}, err
}

// Start the game
func (g *Game) Start() {
	g.State = Started
	log.Info("Game started",
		zap.String("chanID", g.ChanID),
		zap.Int64("seed", g.seed),
		zap.Int("totalRoundPlayed", g.TotalRoundPlayed))

	go func() {
		g.Out <- StateMessage{ChanID: g.ChanID, State: Started}
		DefaultDB.incStats("game_started")
		DefaultDB.incChannelStats(g.ChanID, "game_started")
		for i := 1; i <= RoundPerGame; i++ {
			err := g.startRound(i)
			if err != nil {
				log.Error("starting round failed", zap.String("chanID", g.ChanID), zap.Error(err))
			}
			final := i == RoundPerGame
			g.Out <- RankMessage{ChanID: g.ChanID, Round: i, Rank: g.rank, Final: final}
			if !final {
				time.Sleep(DelayBetweenRound)
			}
		}
		DefaultDB.incStats("game_finished")
		DefaultDB.incChannelStats(g.ChanID, "game_finished")
		g.State = Finished
		g.Out <- StateMessage{ChanID: g.ChanID, State: Finished}
		log.Info("Game finished", zap.String("chanID", g.ChanID))
	}()
}

func (g *Game) startRound(currentRound int) error {
	g.TotalRoundPlayed++
	DefaultDB.incRoundPlayed(g.ChanID)

	questionLimit := DefaultQuestionLimit
	if limitConf, err := DefaultDB.ChannelConfig(g.ChanID, "questionLimit", ""); err == nil && limitConf != "" {
		if limit, err := strconv.ParseInt(limitConf, 10, 64); err == nil {
			questionLimit = int(limit)
		}
	}

	r, err := newRound(g.seed, g.TotalRoundPlayed, g.players, questionLimit)
	if err != nil {
		return err
	}
	DefaultDB.incStats("round_started")
	DefaultDB.incChannelStats(g.ChanID, "round_started")

	g.currentRound = r
	r.state = RoundStarted
	timeUp := time.After(RoundDuration)
	timeLeftTick := time.NewTicker(tickDuration)

	// print question
	g.Out <- StateMessage{ChanID: g.ChanID, State: RoundStarted, Round: currentRound, RoundText: r.questionText(g.ChanID, false)}
	log.Info("Round Started", zap.String("chanID", g.ChanID), zap.Int("questionLimit", questionLimit))

	for {
		select {
		case rawMsg := <-g.In: // new answer coming from player
			started := time.Now()
			msg, ok := rawMsg.(TextMessage)
			if !ok {
				log.Error("Unexpected message type input from client")
				continue
			}

			playerActiveMap.Set(string(msg.Player.ID), struct{}{}, cache.DefaultExpiration)
			log.Debug("startRound got message", zap.String("chanID", g.ChanID), zap.Object("msg", msg))

			answer := msg.Text
			correct, alreadyAnswered, idx := r.answer(msg.Player, answer)
			if !correct {
				if TickAfterWrongAnswer {
					g.Out <- WrongAnswerMessage{ChanID: g.ChanID, TimeLeft: r.timeLeft()}
				}
				gameMsgProcessTimer.UpdateSince(started)
				continue
			}
			if alreadyAnswered {
				log.Debug("already answered", zap.String("chanID", g.ChanID), zap.String("by", string(r.correct[idx])))
				gameMsgProcessTimer.UpdateSince(started)
				continue
			}

			// show correct answer
			DefaultDB.incStats("answer_correct")
			DefaultDB.incChannelStats(g.ChanID, "answer_correct")
			DefaultDB.incPlayerStats(msg.Player.ID, "answer_correct")
			qnaText := r.questionText(g.ChanID, false)
			qnaText.Answers[idx].Highlight = true
			g.Out <- qnaText
			log.Info("answer correct",
				zap.String("playerID", string(msg.Player.ID)),
				zap.String("playerName", msg.Player.Name),
				zap.String("answer", answer),
				zap.Int("questionID", r.q.ID),
				zap.String("chanID", g.ChanID))

			if r.finised() {
				timeLeftTick.Stop()
				r.state = RoundFinished
				g.updateRanking(r.ranking())
				g.Out <- StateMessage{ChanID: g.ChanID, State: RoundFinished, Round: currentRound}
				log.Info("Round finished", zap.String("chanID", g.ChanID), zap.Bool("timeout", false))
				DefaultDB.incStats("round_finished")
				DefaultDB.incChannelStats(g.ChanID, "round_finished")
				gameMsgProcessTimer.UpdateSince(started)

				return nil
			}
			gameMsgProcessTimer.UpdateSince(started)

		case <-timeLeftTick.C: // inform time left
			select {
			case g.Out <- TickMessage{ChanID: g.ChanID, TimeLeft: r.timeLeft()}:
			default:
			}

		case <-timeUp: // time is up
			timeLeftTick.Stop()
			g.State = RoundFinished
			g.updateRanking(r.ranking())
			g.Out <- StateMessage{ChanID: g.ChanID, State: RoundTimeout, Round: currentRound}
			log.Info("Round finished", zap.String("chanID", g.ChanID), zap.Bool("timeout", true))
			showUnAnswered := true
			g.Out <- r.questionText(g.ChanID, showUnAnswered)
			DefaultDB.incStats("round_timeout")
			DefaultDB.incChannelStats(g.ChanID, "round_timeout")

			return nil
		}
	}
}

func (g *Game) updateRanking(r Rank) {
	g.rank = g.rank.Add(r)
	DefaultDB.saveScore(g.ChanID, g.ChanName, r)
}

func (g *Game) CurrentQuestion() Question {
	return g.currentRound.q
}
