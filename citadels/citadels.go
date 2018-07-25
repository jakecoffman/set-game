package citadels

import (
	"github.com/jakecoffman/wg"
	"log"
	"time"
	"runtime/debug"
	"math/rand"
	"encoding/json"
)

type Citadels struct {
	*wg.Game

	Players      []*Player
	playerCursor int

	Turn          int // used to tell whose turn it is to pick roles or whose turn is next
	State         State
	CharacterDeck []int
	DistrictDeck  []int
	Crown         Circular
	Roles         [8]*int // stores who chose what, nil represents no one chose it
	FirstToEight  int `json:",omitempty"`

	Kill int // assassin chose to kill this player
}

type Circular struct {
	Value int
	Max int
}

// Inc acts like ++ in other languages: returns the current value and then increments, however if the
// value is more than the max, the value resets to 0.
func (c *Circular) Inc() int {
	rv := c.Value
	c.Value++
	if c.Value > c.Max {
		c.Value = 0
	}
	return rv
}

type Player struct {
	ws        wg.Connector
	Uuid      string `json:"-"`
	Id        int
	Name      string
	Connected bool
	Ip        string `json:"-"`

	IsBot     bool // TODO implement bots for this game
	HasCrown  bool
	Gold      int
	Hand      []int
	Districts []int

	score int
}

func NewGame(id string) *wg.Game {
	c := &Citadels{
		Players:      []*Player{},
		playerCursor: 1,
	}
	c.Game = wg.NewGame(c, id)
	go c.run()
	c.reset()
	return c.Game
}

func (c *Citadels) reset() {
	c.CharacterDeck = make([]int, len(Characters))
	c.DistrictDeck = make([]int, len(Districts))
	c.Roles = [8]*int{}
	c.Crown = Circular{Value: 0, Max: len(c.Players)}
	c.State = lobby
	for _, p := range c.Players {
		p.Gold = 2
		p.HasCrown = false
	}
}

type State int

const (
	lobby = State(iota)
	roles
	// player turns
	goldOrDraw
	putCardBack
	build
	special
	// game over man
	end
)

// message types
const (
	cmdJoin       = "join"
	cmdLeave      = "leave"
	cmdDisconnect = "disconnect"
	cmdStop       = "stop"
	cmdName       = "name"

	// anyone can do these things
	cmdAddBot    = "addbot"
	cmdRemoveBot = "removebot"
	cmdStart     = "start"

	cmdChoose = "choose"

	// turn actions
	cmdAction = "action"
	cmdBuild  = "build"
	cmdPowers = "powers"
)

func (c *Citadels) run() {
	var cmd *wg.Command

	defer func() {
		if r := recover(); r != nil {
			log.Println("Game crashed", r)
			log.Printf("State: %#v\n", c)
			log.Println("Last command received:", cmd)
			debug.PrintStack()
		}
	}()

	var update bool
	for {
		cmd = <-c.Cmd

		if c.Version != cmd.Version {
			continue
		}

		switch cmd.Type {
		case cmdJoin:
			update = c.handleJoin(cmd)
		case cmdLeave:
			update = c.handleLeave(cmd)
		case cmdDisconnect:
			update = c.handleDisconnect(cmd)
		case cmdStop:
			return
		case cmdName:
			update = c.handleName(cmd)
		case cmdStart:
			update = c.handleStart(cmd)
		case cmdChoose:
			update = c.handleChoose(cmd)
		case cmdAction:
			update = c.handleAction(cmd)
		case cmdBuild:
			update = c.handleBuild(cmd)
		case cmdPowers:
			update = c.handleSpecial(cmd)
		default:
			log.Println("Unknown message:", cmd.Type)
			continue
		}
		if update {
			c.sendEveryoneEverything()
			c.Updated = time.Now()
		}
	}
}

// Find returns the player object and the position they are in
func Find(players []*Player, uuid string) (*Player, int) {
	for i, player := range players {
		if player.Uuid == uuid {
			return player, i
		}
	}
	return nil, -1
}

type MsgMsg struct {
	Type string
	Msg  string
}

func sendMsg(c wg.Connector, msg string) {
	c.Send(&MsgMsg{Type: "msg", Msg: msg})
}

func (c *Citadels) handleJoin(cmd *wg.Command) bool {
	player, i := Find(c.Players, cmd.PlayerId)
	if i == -1 {
		// player was not here before
		if c.State != lobby {
			sendMsg(cmd.Ws, "Can't join game in progress")
			return false
		}
		if len(c.Players) >= 10 {
			// can't have more than 10 players
			sendMsg(cmd.Ws, "Can't have more than 10 players")
			return false
		}
		player = &Player{Uuid: cmd.PlayerId, Id: c.playerCursor}
		c.Players = append(c.Players, player)
		c.playerCursor += 1
	}
	player.ws = cmd.Ws
	player.Connected = true
	player.Ip = player.ws.Ip()
	return true
}

func (c *Citadels) handleLeave(cmd *wg.Command) bool {
	for i, player := range c.Players {
		if player.Uuid == cmd.PlayerId {
			c.Players = append(c.Players[0:i], c.Players[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Citadels) handleDisconnect(cmd *wg.Command) bool {
	player, i := Find(c.Players, cmd.PlayerId)
	if i == -1 {
		log.Println("Couldn't find player", cmd.PlayerId)
		return false
	}
	player.ws = nil
	player.Connected = false
	return true
}

func (c *Citadels) handleStart(cmd *wg.Command) bool {
	if c.Version != cmd.Version {
		sendMsg(cmd.Ws, "Someone else started the game first")
		return false
	}

	if c.State != lobby {
		sendMsg(cmd.Ws, "Illegal state")
		return false
	}

	if len(c.Players) < 2 || len(c.Players) > 7 {
		sendMsg(cmd.Ws, "Need 2-7 players to start the game")
		return false
	}

	// TODO handle game variants, for now this is just the 2 player game

	c.State = roles

	// remove unconnected players and reorder them, leader always starts in position 1
	{
		var newPlayers []*Player
		walk := rand.Perm(len(c.Players))
		for _, i := range walk {
			if !c.Players[i].IsBot && !c.Players[i].Connected {
				continue
			}
			newPlayers = append(newPlayers, c.Players[i])
		}
		c.Players = newPlayers
		c.Crown = Circular{Value: 0, Max: len(c.Players)}
		c.Players[0].HasCrown = true
	}

	// shuffle the decks
	c.CharacterDeck = rand.Perm(len(Characters))
	c.DistrictDeck = rand.Perm(len(Districts))

	// 2 player variant only: discard 1 without anyone seeing
	// TODO other player variants
	c.CharacterDeck = c.CharacterDeck[1:]

	// deal 4 districts to each player
	for _, p := range c.Players {
		p.Hand = c.DistrictDeck[0:4]
		c.DistrictDeck = c.DistrictDeck[4:]
	}

	return true
}

func (c *Citadels) handleName(cmd *wg.Command) bool {
	p, _ := Find(c.Players, cmd.PlayerId)
	if c.State != lobby && p.Name != "" {
		sendMsg(p.ws, "Wait for the lobby to change your name again")
		return false
	}

	var name string
	err := json.Unmarshal(cmd.Data, &name)
	if err != nil {
		log.Println(err)
		sendMsg(p.ws, "Got invalid data for name")
		return false
	}

	if len(name) > 8 {
		p.Name = name[0:8]
	} else {
		p.Name = name
	}

	return true
}

func (c *Citadels) handleChoose(cmd *wg.Command) bool {
	p, i := Find(c.Players, cmd.PlayerId)
	if c.Turn != i || c.State != roles {
		return false
	}

	var choice int
	if err := json.Unmarshal(cmd.Data, choice); err != nil {
		sendMsg(p.ws, "Couldn't decode choice")
		return false
	}

	if choice > len(c.CharacterDeck) || choice < 0 {
		sendMsg(p.ws, "Invalid choice")
		return false
	}

	// two player variant
	switch len(c.CharacterDeck) {
	case 7:
		// player 1 chose their first character, turn over
		c.Roles[c.CharacterDeck[choice]] = &p.Id
		c.CharacterDeck = append(c.CharacterDeck[:choice], c.CharacterDeck[choice+1:]...)
		c.Turn++
		if c.Turn > len(c.Players) {
			c.Turn = 0
		}
	case 6:
		// player 2 chose their first character and gets to go again
		c.Roles[c.CharacterDeck[choice]] = &p.Id
		c.CharacterDeck = append(c.CharacterDeck[:choice], c.CharacterDeck[choice+1:]...)
	case 5:
		// player 2 is discarding this character
		c.CharacterDeck = append(c.CharacterDeck[:choice], c.CharacterDeck[choice+1:]...)
		c.Turn++
		if c.Turn > len(c.Players) {
			c.Turn = 0
		}
	case 4:
		// player 1 chose their second character and gets to go again
		c.Roles[c.CharacterDeck[choice]] = &p.Id
		c.CharacterDeck = append(c.CharacterDeck[:choice], c.CharacterDeck[choice+1:]...)
	case 3:
		// player 1 is discarding this character
		c.CharacterDeck = append(c.CharacterDeck[:choice], c.CharacterDeck[choice+1:]...)
		c.Turn++
		if c.Turn > len(c.Players) {
			c.Turn = 0
		}
	case 2:
		// player 2 chose their second character and this phase is over
		c.Roles[c.CharacterDeck[choice]] = &p.Id
		c.CharacterDeck = []int{}
		c.State = goldOrDraw

		// figure out whose turn it is
		for i, id := range c.Roles {
			if id != nil {
				c.Turn = i // subtle: store the index instead of the ID so we can pick up here later
			}
		}
	default:
		log.Println("Unexpected state:", len(c.CharacterDeck), choice, p, c)
		return false
	}

	return true
}

func (c *Citadels) handleAction(cmd *wg.Command) bool {
	p, id := Find(c.Players, cmd.PlayerId)
	if id != *c.Roles[c.Turn] {
		sendMsg(p.ws, "Not your turn yet")
		return false
	}
	if c.State < goldOrDraw || c.State > putCardBack {
		sendMsg(p.ws, "It's not time for actions")
		return false
	}

	// player can either get 2 gold or draw 2 cards and put one card back
	if c.State == goldOrDraw {
		var choice int
		if err := json.Unmarshal(cmd.Data, &choice); err != nil {
			log.Println(err)
			sendMsg(p.ws, "couldn't unmarshal choice")
			return false
		}
		if choice == 0 {
			p.Gold += 2
			c.State = build
			return true
		}
		// give two cards, they will return one next
		c.State = putCardBack
		p.Hand = append(p.Hand, c.DistrictDeck[:2]...)
		return true
	}

	var choice int
	if err := json.Unmarshal(cmd.Data, &choice); err != nil {
		log.Println(err)
		sendMsg(p.ws, "couldn't unmarshal choice")
		return false
	}
	// if they chose 0, swap the last two cards
	if choice == 0 {
		p.Hand[len(p.Hand)-2] = p.Hand[len(p.Hand)-1]
	}
	// drop the last card off their deck
	p.Hand = p.Hand[:len(p.Hand)-1]
	c.State = build
	return true
}

func (c *Citadels) handleBuild(cmd *wg.Command) bool {
	p, id := Find(c.Players, cmd.PlayerId)
	if id != *c.Roles[c.Turn] {
		sendMsg(p.ws, "Not your turn yet")
		return false
	}
	if c.State != build {
		sendMsg(p.ws, "It's not time to build")
		return false
	}

	var choice int
	if err := json.Unmarshal(cmd.Data, &choice); err != nil {
		sendMsg(p.ws, "Couldn't unmarshal choice")
		return false
	}

	if choice == -1 {
		c.State = special
		return true
	}

	chosenDistrict := Districts[p.Hand[choice]]
	if p.Gold < chosenDistrict.Value {
		sendMsg(p.ws, "You can't afford that district")
		return false
	}
	for _, district := range p.Districts {
		if Districts[district].Name == chosenDistrict.Name {
			sendMsg(p.ws, "Can't have duplicate districts")
			return false
		}
	}

	p.Gold -= chosenDistrict.Value
	p.Districts = append(p.Districts, p.Hand[choice])
	p.Hand[choice] = p.Hand[len(p.Hand)-1]
	p.Hand = p.Hand[:len(p.Hand)-1]
	c.State = special

	// TODO: handle the case where the magician has already used their special

	return true
}

func (c *Citadels) handleSpecial(cmd *wg.Command) bool {
	p, id := Find(c.Players, cmd.PlayerId)
	if id != *c.Roles[c.Turn] {
		sendMsg(p.ws, "Not your turn yet")
		return false
	}

	var choice int
	if err := json.Unmarshal(cmd.Data, &choice); err != nil {
		sendMsg(p.ws, "Couldn't unmarshal choice")
		return false
	}

	if !Characters[c.Turn](c, p, choice) {
		return false
	}

	// next player's turn
	for i := c.Turn+1; i < len(Characters); i++ {
		if c.Roles[i] != nil {
			c.Turn = i
			return true
		}
	}

	// end of round, check for win condition and winner

	for _, p := range c.Players {
		p.score = 0
		colors := [5]int{}
		for _, d := range p.Districts {
			card := Districts[d]
			p.score += card.Value
			colors[int(card.Color)]++
		}
		allColors := true
		for _, c := range colors {
			if c == 0 {
				allColors = false
				break
			}
		}
		if allColors {
			p.score += 3
		}
		if c.FirstToEight == p.Id {
			p.score += 4
		}
		if len(p.Districts) >= 8 {
			c.State = end
		}
	}

	if c.State != end {
		c.State = roles
		c.Turn = c.Crown.Inc()
	}

	return true
}

type UpdateMsg struct {
	Type   string
	Update *Citadels
	You    *secret
}

type secret struct {
	Id       int
	HasCrown bool
}

func (c *Citadels) sendEveryoneEverything() {
	for _, p := range c.Players {
		if p.ws != nil {
			msg := &UpdateMsg{Type: "all", Update: c}
			msg.You = &secret{Id: p.Id, HasCrown: p.HasCrown}
			p.ws.Send(msg)
		}
	}
}