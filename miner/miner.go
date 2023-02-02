package miner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log/v2"
	"go.opencensus.io/trace"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/go-state-types/proof"

	"github.com/filecoin-project/lotus/api"
	lapi "github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/v1api"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/actors/builtin"
	"github.com/filecoin-project/lotus/chain/actors/policy"
	"github.com/filecoin-project/lotus/chain/gen"
	"github.com/filecoin-project/lotus/chain/gen/slashfilter"
	lrand "github.com/filecoin-project/lotus/chain/rand"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/journal"
)

type rel struct {
	ver     int
	aggr    address.Address
	pieceID string
}

type target struct {
	ver_sum   int
	cur_sum   int
	cur_proof [32][]byte
}

var log = logging.Logger("miner")
var relation map[string]rel = make(map[string]rel)
var aggr_tag map[string]target = make(map[string]target)

// Journal event types.
const (
	evtTypeBlockMined = iota
)

// waitFunc is expected to pace block mining at the configured network rate.
//
// baseTime is the timestamp of the mining base, i.e. the timestamp
// of the tipset we're planning to construct upon.
//
// Upon each mining loop iteration, the returned callback is called reporting
// whether we mined a block in this round or not.
type waitFunc func(ctx context.Context, baseTime uint64) (func(bool, abi.ChainEpoch, error), abi.ChainEpoch, error)

func randTimeOffset(width time.Duration) time.Duration {
	buf := make([]byte, 8)
	rand.Reader.Read(buf) //nolint:errcheck
	val := time.Duration(binary.BigEndian.Uint64(buf) % uint64(width))

	return val - (width / 2)
}

// NewMiner instantiates a miner with a concrete WinningPoStProver and a miner
// address (which can be different from the worker's address).
func NewMiner(api v1api.FullNode, epp gen.WinningPoStProver, addr address.Address, sf *slashfilter.SlashFilter, j journal.Journal) *Miner {
	arc, err := lru.NewARC(10000)
	if err != nil {
		panic(err)
	}

	return &Miner{
		api:     api,
		epp:     epp,
		address: addr,
		waitFunc: func(ctx context.Context, baseTime uint64) (func(bool, abi.ChainEpoch, error), abi.ChainEpoch, error) {
			// wait around for half the block time in case other parents come in
			//
			// if we're mining a block in the past via catch-up/rush mining,
			// such as when recovering from a network halt, this sleep will be
			// for a negative duration, and therefore **will return
			// immediately**.
			//
			// the result is that we WILL NOT wait, therefore fast-forwarding
			// and thus healing the chain by backfilling it with null rounds
			// rapidly.
			deadline := baseTime + build.PropagationDelaySecs
			baseT := time.Unix(int64(deadline), 0)

			baseT = baseT.Add(randTimeOffset(time.Second))

			build.Clock.Sleep(build.Clock.Until(baseT))

			return func(bool, abi.ChainEpoch, error) {}, 0, nil
		},

		sf:                sf,
		minedBlockHeights: arc,
		evtTypes: [...]journal.EventType{
			evtTypeBlockMined: j.RegisterEventType("miner", "block_mined"),
		},
		journal: j,
	}
}

// Miner encapsulates the mining processes of the system.
//
// Refer to the godocs on mineOne and mine methods for more detail.
type Miner struct {
	api v1api.FullNode

	epp gen.WinningPoStProver

	lk       sync.Mutex
	address  address.Address
	stop     chan struct{}
	stopping chan struct{}

	waitFunc waitFunc

	// lastWork holds the last MiningBase we built upon.
	lastWork *MiningBase

	sf *slashfilter.SlashFilter
	// minedBlockHeights is a safeguard that caches the last heights we mined.
	// It is consulted before publishing a newly mined block, for a sanity check
	// intended to avoid slashings in case of a bug.
	minedBlockHeights *lru.ARCCache

	evtTypes [1]journal.EventType
	journal  journal.Journal
}

// Address returns the address of the miner.
func (m *Miner) Address() address.Address {
	m.lk.Lock()
	defer m.lk.Unlock()

	return m.address
}

// Start starts the mining operation. It spawns a goroutine and returns
// immediately. Start is not idempotent.
func (m *Miner) Start(_ context.Context) error {
	m.lk.Lock()
	defer m.lk.Unlock()
	if m.stop != nil {
		return fmt.Errorf("miner already started")
	}
	m.stop = make(chan struct{})
	go m.mine(context.TODO())
	return nil
}

// Stop stops the mining operation. It is not idempotent, and multiple adjacent
// calls to Stop will fail.
func (m *Miner) Stop(ctx context.Context) error {
	m.lk.Lock()

	m.stopping = make(chan struct{})
	stopping := m.stopping
	close(m.stop)

	m.lk.Unlock()

	select {
	case <-stopping:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Miner) niceSleep(d time.Duration) bool {
	select {
	case <-build.Clock.After(d):
		return true
	case <-m.stop:
		log.Infow("received interrupt while trying to sleep in mining cycle")
		return false
	}
}

// mine runs the mining loop. It performs the following:
//
//  1. Queries our current best currently-known mining candidate (tipset to
//     build upon).
//  2. Waits until the propagation delay of the network has elapsed (currently
//     6 seconds). The waiting is done relative to the timestamp of the best
//     candidate, which means that if it's way in the past, we won't wait at
//     all (e.g. in catch-up or rush mining).
//  3. After the wait, we query our best mining candidate. This will be the one
//     we'll work with.
//  4. Sanity check that we _actually_ have a new mining base to mine on. If
//     not, wait one epoch + propagation delay, and go back to the top.
//  5. We attempt to mine a block, by calling mineOne (refer to godocs). This
//     method will either return a block if we were eligible to mine, or nil
//     if we weren't.
//     6a. If we mined a block, we update our state and push it out to the network
//     via gossipsub.
//     6b. If we didn't mine a block, we consider this to be a nil round on top of
//     the mining base we selected. If other miner or miners on the network
//     were eligible to mine, we will receive their blocks via gossipsub and
//     we will select that tipset on the next iteration of the loop, thus
//     discarding our null round.
func (m *Miner) mine(ctx context.Context) {
	ctx, span := trace.StartSpan(ctx, "/mine")
	defer span.End()

	go m.doWinPoStWarmup(ctx)

	var lastBase MiningBase
minerLoop:
	for {
		select {
		case <-m.stop:
			stopping := m.stopping
			m.stop = nil
			m.stopping = nil
			close(stopping)
			return

		default:
		}

		var base *MiningBase
		var onDone func(bool, abi.ChainEpoch, error)
		var injectNulls abi.ChainEpoch

		for {
			prebase, err := m.GetBestMiningCandidate(ctx)
			if err != nil {
				log.Errorf("failed to get best mining candidate: %s", err)
				if !m.niceSleep(time.Second * 5) {
					continue minerLoop
				}
				continue
			}

			if base != nil && base.TipSet.Height() == prebase.TipSet.Height() && base.NullRounds == prebase.NullRounds {
				base = prebase
				break
			}
			if base != nil {
				onDone(false, 0, nil)
			}

			// TODO: need to change the orchestration here. the problem is that
			// we are waiting *after* we enter this loop and selecta mining
			// candidate, which is almost certain to change in multiminer
			// tests. Instead, we should block before entering the loop, so
			// that when the test 'MineOne' function is triggered, we pull our
			// best mining candidate at that time.

			// Wait until propagation delay period after block we plan to mine on
			onDone, injectNulls, err = m.waitFunc(ctx, prebase.TipSet.MinTimestamp())
			if err != nil {
				log.Error(err)
				continue
			}

			// just wait for the beacon entry to become available before we select our final mining base
			_, err = m.api.StateGetBeaconEntry(ctx, prebase.TipSet.Height()+prebase.NullRounds+1)
			if err != nil {
				log.Errorf("failed getting beacon entry: %s", err)
				if !m.niceSleep(time.Second) {
					continue minerLoop
				}
				continue
			}

			base = prebase
		}

		base.NullRounds += injectNulls // testing

		if base.TipSet.Equals(lastBase.TipSet) && lastBase.NullRounds == base.NullRounds {
			log.Warnf("BestMiningCandidate from the previous round: %s (nulls:%d)", lastBase.TipSet.Cids(), lastBase.NullRounds)
			if !m.niceSleep(time.Duration(build.BlockDelaySecs) * time.Second) {
				continue minerLoop
			}
			continue
		}

		b, err := m.mineOne(ctx, base)
		if err != nil {
			log.Errorf("mining block failed: %+v", err)
			if !m.niceSleep(time.Second) {
				continue minerLoop
			}
			onDone(false, 0, err)
			continue
		}
		lastBase = *base

		var h abi.ChainEpoch
		if b != nil {
			h = b.Header.Height
		}
		onDone(b != nil, h, nil)

		if b != nil {
			m.journal.RecordEvent(m.evtTypes[evtTypeBlockMined], func() interface{} {
				return map[string]interface{}{
					"parents":   base.TipSet.Cids(),
					"nulls":     base.NullRounds,
					"epoch":     b.Header.Height,
					"timestamp": b.Header.Timestamp,
					"cid":       b.Header.Cid(),
				}
			})

			btime := time.Unix(int64(b.Header.Timestamp), 0)
			now := build.Clock.Now()
			switch {
			case btime == now:
				// block timestamp is perfectly aligned with time.
			case btime.After(now):
				if !m.niceSleep(build.Clock.Until(btime)) {
					log.Warnf("received interrupt while waiting to broadcast block, will shutdown after block is sent out")
					build.Clock.Sleep(build.Clock.Until(btime))
				}
			default:
				log.Warnw("mined block in the past",
					"block-time", btime, "time", build.Clock.Now(), "difference", build.Clock.Since(btime))
			}

			if err := m.sf.MinedBlock(ctx, b.Header, base.TipSet.Height()+base.NullRounds); err != nil {
				log.Errorf("<!!> SLASH FILTER ERROR: %s", err)
				if os.Getenv("LOTUS_MINER_NO_SLASHFILTER") != "_yes_i_know_i_can_and_probably_will_lose_all_my_fil_and_power_" {
					continue
				}
			}

			blkKey := fmt.Sprintf("%d", b.Header.Height)
			if _, ok := m.minedBlockHeights.Get(blkKey); ok {
				log.Warnw("Created a block at the same height as another block we've created", "height", b.Header.Height, "miner", b.Header.Miner, "parents", b.Header.Parents)
				continue
			}

			m.minedBlockHeights.Add(blkKey, true)

			if err := m.api.SyncSubmitBlock(ctx, b); err != nil {
				log.Errorf("failed to submit newly mined block: %+v", err)
			}
		} else {
			base.NullRounds++

			// Wait until the next epoch, plus the propagation delay, so a new tipset
			// has enough time to form.
			//
			// See:  https://github.com/filecoin-project/lotus/issues/1845
			nextRound := time.Unix(int64(base.TipSet.MinTimestamp()+build.BlockDelaySecs*uint64(base.NullRounds))+int64(build.PropagationDelaySecs), 0)

			select {
			case <-build.Clock.After(build.Clock.Until(nextRound)):
			case <-m.stop:
				stopping := m.stopping
				m.stop = nil
				m.stopping = nil
				close(stopping)
				return
			}
		}
	}
}

// MiningBase is the tipset on top of which we plan to construct our next block.
// Refer to godocs on GetBestMiningCandidate.
type MiningBase struct {
	TipSet     *types.TipSet
	NullRounds abi.ChainEpoch
}

// GetBestMiningCandidate implements the fork choice rule from a miner's
// perspective.
//
// It obtains the current chain head (HEAD), and compares it to the last tipset
// we selected as our mining base (LAST). If HEAD's weight is larger than
// LAST's weight, it selects HEAD to build on. Else, it selects LAST.
func (m *Miner) GetBestMiningCandidate(ctx context.Context) (*MiningBase, error) {
	m.lk.Lock()
	defer m.lk.Unlock()

	bts, err := m.api.ChainHead(ctx)
	if err != nil {
		return nil, err
	}

	if m.lastWork != nil {
		if m.lastWork.TipSet.Equals(bts) {
			return m.lastWork, nil
		}

		btsw, err := m.api.ChainTipSetWeight(ctx, bts.Key())
		if err != nil {
			return nil, err
		}
		ltsw, err := m.api.ChainTipSetWeight(ctx, m.lastWork.TipSet.Key())
		if err != nil {
			m.lastWork = nil
			return nil, err
		}

		if types.BigCmp(btsw, ltsw) <= 0 {
			return m.lastWork, nil
		}
	}

	m.lastWork = &MiningBase{TipSet: bts}
	return m.lastWork, nil
}

// mineOne attempts to mine a single block, and does so synchronously, if and
// only if we are eligible to mine.
//
// {hint/landmark}: This method coordinates all the steps involved in mining a
// block, including the condition of whether mine or not at all depending on
// whether we win the round or not.
//
// This method does the following:
//
//	1.
func (m *Miner) mineOne(ctx context.Context, base *MiningBase) (minedBlock *types.BlockMsg, err error) {
	log.Debugw("attempting to mine a block", "tipset", types.LogCids(base.TipSet.Cids()))
	tStart := build.Clock.Now()

	round := base.TipSet.Height() + base.NullRounds + 1

	// always write out a log
	var winner *types.ElectionProof
	var mbi *api.MiningBaseInfo
	var rbase types.BeaconEntry
	defer func() {

		var hasMinPower bool

		// mbi can be nil if we are deep in penalty and there are 0 eligible sectors
		// in the current deadline. If this case - put together a dummy one for reporting
		// https://github.com/filecoin-project/lotus/blob/v1.9.0/chain/stmgr/utils.go#L500-L502
		if mbi == nil {
			mbi = &api.MiningBaseInfo{
				NetworkPower:      big.NewInt(-1), // we do not know how big the network is at this point
				EligibleForMining: false,
				MinerPower:        big.NewInt(0), // but we do know we do not have anything eligible
			}

			// try to opportunistically pull actual power and plug it into the fake mbi
			if pow, err := m.api.StateMinerPower(ctx, m.address, base.TipSet.Key()); err == nil && pow != nil {
				hasMinPower = pow.HasMinPower
				mbi.MinerPower = pow.MinerPower.QualityAdjPower
				mbi.NetworkPower = pow.TotalPower.QualityAdjPower
			}
		}

		isLate := uint64(tStart.Unix()) > (base.TipSet.MinTimestamp() + uint64(base.NullRounds*builtin.EpochDurationSeconds) + build.PropagationDelaySecs)

		logStruct := []interface{}{
			"tookMilliseconds", (build.Clock.Now().UnixNano() - tStart.UnixNano()) / 1_000_000,
			"forRound", int64(round),
			"baseEpoch", int64(base.TipSet.Height()),
			"baseDeltaSeconds", uint64(tStart.Unix()) - base.TipSet.MinTimestamp(),
			"nullRounds", int64(base.NullRounds),
			"lateStart", isLate,
			"beaconEpoch", rbase.Round,
			"lookbackEpochs", int64(policy.ChainFinality), // hardcoded as it is unlikely to change again: https://github.com/filecoin-project/lotus/blob/v1.8.0/chain/actors/policy/policy.go#L180-L186
			"networkPowerAtLookback", mbi.NetworkPower.String(),
			"minerPowerAtLookback", mbi.MinerPower.String(),
			"isEligible", mbi.EligibleForMining,
			"isWinner", (winner != nil),
			"error", err,
		}

		if err != nil {
			log.Errorw("completed mineOne", logStruct...)
		} else if isLate || (hasMinPower && !mbi.EligibleForMining) {
			log.Warnw("completed mineOne", logStruct...)
		} else {
			log.Infow("completed mineOne", logStruct...)
		}
	}()

	mbi, err = m.api.MinerGetBaseInfo(ctx, m.address, round, base.TipSet.Key())
	if err != nil {
		err = xerrors.Errorf("failed to get mining base info: %w", err)
		return nil, err
	}
	if mbi == nil {
		return nil, nil
	}

	if !mbi.EligibleForMining {
		// slashed or just have no power yet
		return nil, nil
	}

	tPowercheck := build.Clock.Now()

	bvals := mbi.BeaconEntries
	rbase = mbi.PrevBeaconEntry
	if len(bvals) > 0 {
		rbase = bvals[len(bvals)-1]
	}

	ticket, err := m.computeTicket(ctx, &rbase, base, mbi)
	if err != nil {
		err = xerrors.Errorf("scratching ticket failed: %w", err)
		return nil, err
	}

	winner, err = gen.IsRoundWinner(ctx, base.TipSet, round, m.address, rbase, mbi, m.api)
	if err != nil {
		err = xerrors.Errorf("failed to check if we win next round: %w", err)
		return nil, err
	}

	if winner == nil {
		return nil, nil
	}

	tTicket := build.Clock.Now()

	buf := new(bytes.Buffer)
	if err := m.address.MarshalCBOR(buf); err != nil {
		err = xerrors.Errorf("failed to marshal miner address: %w", err)
		return nil, err
	}

	rand, err := lrand.DrawRandomness(rbase.Data, crypto.DomainSeparationTag_WinningPoStChallengeSeed, round, buf.Bytes())
	if err != nil {
		err = xerrors.Errorf("failed to get randomness for winning post: %w", err)
		return nil, err
	}

	prand := abi.PoStRandomness(rand)

	tSeed := build.Clock.Now()
	nv, err := m.api.StateNetworkVersion(ctx, base.TipSet.Key())
	if err != nil {
		return nil, err
	}

	postProof, err := m.epp.ComputeProof(ctx, mbi.Sectors, prand, round, nv)
	if err != nil {
		err = xerrors.Errorf("failed to compute winning post proof: %w", err)
		return nil, err
	}

	tProof := build.Clock.Now()

	// get pending messages early,
	msgs, err := m.api.MpoolSelect(context.TODO(), base.TipSet.Key(), ticket.Quality())
	if err != nil {
		err = xerrors.Errorf("failed to select messages for block: %w", err)
		return nil, err
	}

	for _, msg := range msgs {
		log.Infof("message selected:type:%v, value: %v\n", msg.Message.Method, msg.Message.Value)
		if msg.Message.Method == 0 && msg.Message.To == m.address {
			err := m.HandleMessageType_0(ctx, msg)
			if err != nil {
				return nil, err
			}
		}
	}

	tPending := build.Clock.Now()

	// TODO: winning post proof
	minedBlock, err = m.createBlock(base, m.address, ticket, winner, bvals, postProof, msgs)
	if err != nil {
		err = xerrors.Errorf("failed to create block: %w", err)
		return nil, err
	}

	tCreateBlock := build.Clock.Now()
	dur := tCreateBlock.Sub(tStart)
	parentMiners := make([]address.Address, len(base.TipSet.Blocks()))
	for i, header := range base.TipSet.Blocks() {
		parentMiners[i] = header.Miner
	}
	messagecontained := len(minedBlock.BlsMessages) + len(minedBlock.SecpkMessages)
	log.Infow("mined new block", "cid", minedBlock.Cid(), "height", int64(minedBlock.Header.Height), "miner", minedBlock.Header.Miner, "parents", parentMiners, "parentTipset", base.TipSet.Key().String(), "took", dur, "containmsgs", messagecontained, "selected message", len(msgs))
	if dur > time.Second*time.Duration(build.BlockDelaySecs) {
		log.Warnw("CAUTION: block production took longer than the block delay. Your computer may not be fast enough to keep up",
			"tPowercheck ", tPowercheck.Sub(tStart),
			"tTicket ", tTicket.Sub(tPowercheck),
			"tSeed ", tSeed.Sub(tTicket),
			"tProof ", tProof.Sub(tSeed),
			"tPending ", tPending.Sub(tProof),
			"tCreateBlock ", tCreateBlock.Sub(tPending))
	}

	return minedBlock, nil
}

func (m *Miner) computeTicket(ctx context.Context, brand *types.BeaconEntry, base *MiningBase, mbi *api.MiningBaseInfo) (*types.Ticket, error) {
	buf := new(bytes.Buffer)
	if err := m.address.MarshalCBOR(buf); err != nil {
		return nil, xerrors.Errorf("failed to marshal address to cbor: %w", err)
	}

	round := base.TipSet.Height() + base.NullRounds + 1
	if round > build.UpgradeSmokeHeight {
		buf.Write(base.TipSet.MinTicket().VRFProof)
	}

	input, err := lrand.DrawRandomness(brand.Data, crypto.DomainSeparationTag_TicketProduction, round-build.TicketRandomnessLookback, buf.Bytes())
	if err != nil {
		return nil, err
	}

	vrfOut, err := gen.ComputeVRF(ctx, m.api.WalletSign, mbi.WorkerKey, input)
	if err != nil {
		return nil, err
	}

	return &types.Ticket{
		VRFProof: vrfOut,
	}, nil
}

func (m *Miner) createBlock(base *MiningBase, addr address.Address, ticket *types.Ticket,
	eproof *types.ElectionProof, bvals []types.BeaconEntry, wpostProof []proof.PoStProof, msgs []*types.SignedMessage) (*types.BlockMsg, error) {
	uts := base.TipSet.MinTimestamp() + build.BlockDelaySecs*(uint64(base.NullRounds)+1)

	nheight := base.TipSet.Height() + base.NullRounds + 1

	// why even return this? that api call could just submit it for us
	return m.api.MinerCreateBlock(context.TODO(), &api.BlockTemplate{
		Miner:            addr,
		Parents:          base.TipSet.Key(),
		Ticket:           ticket,
		Eproof:           eproof,
		BeaconValues:     bvals,
		Messages:         msgs,
		Epoch:            nheight,
		Timestamp:        uts,
		WinningPoStProof: wpostProof,
	})
}

func (m *Miner) HandleMessageType_0(ctx context.Context, msg *types.SignedMessage) error {
	if msg.Message.Value.Int.Int64() <= 32 {
		//this case is the increment register message, we just support the file version under the 32 as a simple PC
		//can only successfully generate SNARK proof under this case
		//we only handle the message send to us and if we get the other node's message we resend it
		//we will save the relation between the received file and the original file
		//also, if the file is a original file, we will also consider it a aggregate target and save this message
		if msg.Message.To != m.address {
			maxFee := types.MustParseFIL("0.05")
			m.api.MpoolPushMessage(ctx, &msg.Message, &lapi.MessageSendSpec{MaxFee: big.Int(maxFee)})
		} else {
			file_version := msg.Message.Value.Int.Int64()
			log.Info("get the increment file version: ", file_version)
			param := msg.Message.Params
			var lengt int32
			pointer := 0
			len_B := param[pointer : pointer+4]
			dec_buffer1 := bytes.NewBuffer(len_B)
			binary.Read(dec_buffer1, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_addr_B := param[pointer : pointer+int(lengt)]
			dec_addr, _ := address.NewFromBytes(dec_addr_B)
			log.Info("decode aggregator: ", dec_addr)

			pointer = pointer + int(lengt)
			len_B = param[pointer : pointer+4]
			dec_buffer2 := bytes.NewBuffer(len_B)
			binary.Read(dec_buffer2, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_deal_B := param[pointer : pointer+int(lengt)]
			log.Info("decode itself commP: ", string(dec_deal_B))

			pointer = pointer + int(lengt)
			len_B = param[pointer : pointer+4]
			dec_buffer3 := bytes.NewBuffer(len_B)
			binary.Read(dec_buffer3, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_ori_file_B := param[pointer : pointer+int(lengt)]
			log.Info("decode original file commP: ", string(dec_ori_file_B))

			pieceCID := string(dec_deal_B)
			message := &rel{ver: int(file_version), aggr: dec_addr, pieceID: string(dec_ori_file_B)}
			relation[pieceCID] = *message

			if file_version == 0 {
				pieceCID := string(dec_deal_B)
				message := &target{ver_sum: 1, cur_sum: 0}
				aggr_tag[pieceCID] = *message
				log.Info("test the change of aggregate message: ver_sum: ", message.ver_sum, " cur_sum: ", message.cur_sum, " proof size: ", len(message.cur_proof[0]))
			}
		}
		//save this informations into a map: Map[increment_commP]->(version, aggregator. original_file_commP)
	} else if msg.Message.Value.Int.Int64() >= 500 {
		//register message: if a storage provider can generate a porep proof, it means that this is an
		//honest provider and can join the aggregate process
		//we only handle the message send to us and if we get the other node's message we resend it
		//
		if msg.Message.To != m.address {
			maxFee := types.MustParseFIL("0.05")
			m.api.MpoolPushMessage(ctx, &msg.Message, &lapi.MessageSendSpec{MaxFee: big.Int(maxFee)})
		} else {
			param := msg.Message.Params
			var lengt int32
			pointer := 0
			len_B1 := param[pointer : pointer+4]
			dec_buffer1 := bytes.NewBuffer(len_B1)
			binary.Read(dec_buffer1, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_proof := param[pointer : pointer+int(lengt)]
			//dec_addr, _ := address.NewFromBytes(dec_addr_B)
			log.Info("decode porep proof: ", dec_proof)

			pointer = pointer + int(lengt)
			len_B2 := param[pointer : pointer+4]
			dec_buffer2 := bytes.NewBuffer(len_B2)
			binary.Read(dec_buffer2, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_commP_B := param[pointer : pointer+int(lengt)]
			commP, _ := cid.Cast(dec_commP_B)
			log.Info("decode commP itself: ", commP.String()) //use this to check whether this is an increment or not.

			message, result := relation[commP.String()]
			if result {
				if message.aggr != m.address {
					var para []byte

					para = append(para, len_B1...)
					para = append(para, dec_proof...)
					ori_file := []byte(message.pieceID)
					len_ori_file := len(ori_file)
					buffer1 := bytes.NewBuffer([]byte{})
					binary.Write(buffer1, binary.BigEndian, int32(len_ori_file))
					len_pieceCID := buffer1.Bytes()
					para = append(para, len_pieceCID...)
					para = append(para, ori_file...)

					msg := &types.Message{
						From:   m.address,
						To:     message.aggr,
						Method: 0,
						Params: para,
						Value:  types.NewInt(uint64(500 + message.ver)),
					}
					maxFee := types.MustParseFIL("0.05")
					m.api.MpoolPushMessage(ctx, msg, &lapi.MessageSendSpec{MaxFee: big.Int(maxFee)})

				} else {
					log.Info(commP.String(), " I got you!")
					aggr_msg, result2 := aggr_tag[message.pieceID]
					if !result2 {
						log.Debug("this could not happen when handle porep register message")
					}
					pos := int64(0)
					if msg.Message.Value.Int.Int64() > 500 {
						pos = msg.Message.Value.Int.Int64() - 500
					} else {
						pos = int64(message.ver)
					}
					temp := aggr_msg.cur_proof
					append_proof := dec_proof[:32]
					temp[pos] = append(temp[pos], append_proof...)
					new_ver_sum := 0
					if pos+1 > int64(aggr_msg.ver_sum) {
						new_ver_sum = int(pos + 1)
					} else {
						new_ver_sum = aggr_msg.ver_sum
					}
					new_cur_sum := aggr_msg.cur_sum + 1
					new_msg := &target{ver_sum: new_ver_sum, cur_sum: new_cur_sum, cur_proof: temp}
					aggr_tag[message.pieceID] = *new_msg

					aggr_msg, result2 = aggr_tag[message.pieceID]
					if !result2 {
						log.Debug("this could not happen when handle porep register message")
					}
					log.Info("test the change of aggregate message: ver_sum: ", aggr_msg.ver_sum, " cur_sum: ", aggr_msg.cur_sum, " proof size: ", len(aggr_msg.cur_proof[0]))
					if aggr_msg.cur_sum == aggr_msg.ver_sum {
						//save the collected message and clear the data
						filename := "agger_data_ver" + strconv.Itoa(aggr_msg.ver_sum)
						file, _ := os.Create(filename)
						for i := 0; i < aggr_msg.ver_sum; i++ {
							position := strconv.Itoa(i) + " "
							file.WriteString(position)
							for j := 0; j < 4; j++ {
								B_int := aggr_msg.cur_proof[i][8*j : 8*j+8]
								data := binary.LittleEndian.Uint64(B_int)
								str_data := strconv.Itoa(int(data))
								file.WriteString(str_data)
							}
							file.WriteString("\n")
						}
						temp_agge_msg := &target{ver_sum: aggr_msg.ver_sum, cur_sum: 0}
						aggr_tag[message.pieceID] = *temp_agge_msg
					}
					//save
				}
			} else { //no way to happen
				log.Debug("not find the nessage in the storage when deal with the register message")
			}
		}
	} else if msg.Message.Value.Int.Int64() >= 233 && msg.Message.Value.Int.Int64() < 500 {
		if msg.Message.To != m.address {
			maxFee := types.MustParseFIL("0.05")
			m.api.MpoolPushMessage(ctx, &msg.Message, &lapi.MessageSendSpec{MaxFee: big.Int(maxFee)})
		} else {
			param := msg.Message.Params
			var lengt int32
			pointer := 0
			len_B1 := param[pointer : pointer+4]
			dec_buffer1 := bytes.NewBuffer(len_B1)
			binary.Read(dec_buffer1, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_proof := param[pointer : pointer+int(lengt)]
			//dec_addr, _ := address.NewFromBytes(dec_addr_B)
			log.Info("decode PoSt proof: ", dec_proof)

			pointer = pointer + int(lengt)
			len_B2 := param[pointer : pointer+4]
			dec_buffer2 := bytes.NewBuffer(len_B2)
			binary.Read(dec_buffer2, binary.BigEndian, &lengt)

			pointer = pointer + 4
			dec_commP_B := param[pointer : pointer+int(lengt)]
			commP, _ := cid.Cast(dec_commP_B)
			log.Info("decode piece commP in sector: ", commP.String())

			message, result := relation[commP.String()]
			if result {
				if message.aggr != m.address {
					var para []byte

					para = append(para, len_B1...)
					para = append(para, dec_proof...)
					ori_file := []byte(message.pieceID)
					len_ori_file := len(ori_file)
					buffer1 := bytes.NewBuffer([]byte{})
					binary.Write(buffer1, binary.BigEndian, int32(len_ori_file))
					len_pieceCID := buffer1.Bytes()
					para = append(para, len_pieceCID...)
					para = append(para, ori_file...)

					msg := &types.Message{
						From:   m.address,
						To:     message.aggr,
						Method: 0,
						Params: para,
						Value:  types.NewInt(uint64(233 + message.ver)),
					}
					maxFee := types.MustParseFIL("0.05")
					m.api.MpoolPushMessage(ctx, msg, &lapi.MessageSendSpec{MaxFee: big.Int(maxFee)})
				} else {
					log.Info(commP.String(), " I got you!")
					aggr_msg, result2 := aggr_tag[message.pieceID]
					if !result2 {
						log.Debug("this could not happen when handle porep register message")
					}
					pos := int64(0)
					if msg.Message.Value.Int.Int64() > 233 {
						pos = msg.Message.Value.Int.Int64() - 233
					} else {
						pos = int64(message.ver)
					}
					temp := aggr_msg.cur_proof
					append_proof := dec_proof[:32]
					temp[pos] = append(temp[pos], append_proof...)
					new_ver_sum := 0
					if pos+1 > int64(aggr_msg.ver_sum) {
						new_ver_sum = int(pos + 1)
					} else {
						new_ver_sum = aggr_msg.ver_sum
					}
					new_cur_sum := aggr_msg.cur_sum + 1
					new_msg := &target{ver_sum: new_ver_sum, cur_sum: new_cur_sum, cur_proof: temp}
					aggr_tag[message.pieceID] = *new_msg

					aggr_msg, result2 = aggr_tag[message.pieceID]
					if !result2 {
						log.Debug("this could not happen when handle porep register message")
					}
					log.Info("test the change of aggregate message: ver_sum: ", aggr_msg.ver_sum, " cur_sum: ", aggr_msg.cur_sum, " proof size: ", len(aggr_msg.cur_proof[0]))
					if aggr_msg.cur_sum == aggr_msg.ver_sum {
						//go zk-snark proof and send aggregate message
						filename := "agger_data_ver" + strconv.Itoa(aggr_msg.ver_sum)
						file, _ := os.Create(filename)
						for i := 0; i < aggr_msg.ver_sum; i++ {
							position := strconv.Itoa(i) + " "
							file.WriteString(position)
							for j := 0; j < 4; j++ {
								B_int := aggr_msg.cur_proof[i][8*j : 8*j+8]
								data := binary.LittleEndian.Uint64(B_int)
								str_data := strconv.Itoa(int(data))
								file.WriteString(str_data)
							}
							file.WriteString("\n")
						}
						temp_agge_msg := &target{ver_sum: aggr_msg.ver_sum, cur_sum: 0}
						aggr_tag[message.pieceID] = *temp_agge_msg
					}
					t := time.Now()
					file, _ := os.Create("aggr_get_" + t.String())
					defer file.Close()
					file.WriteString("aggr get, timestrap: " + t.String() + "\n" + "pieceCID: " + string(dec_commP_B))
					//save
				}
			} else { //no way to happen
				log.Debug("not find the nessage in the storage when deal with the register message")
			}
		}
	}
	return nil
}
