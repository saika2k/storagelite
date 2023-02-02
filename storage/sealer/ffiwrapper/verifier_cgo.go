//go:build cgo
// +build cgo

package ffiwrapper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"time"

	"github.com/ipfs/go-cid"
	"go.opencensus.io/trace"
	"golang.org/x/xerrors"

	ffi "github.com/filecoin-project/filecoin-ffi"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-actors/v7/actors/runtime/proof"

	"github.com/filecoin-project/lotus/storage/sealer/storiface"
	mc "github.com/multiformats/go-multicodec"
	mh "github.com/multiformats/go-multihash"
	varint "github.com/multiformats/go-varint"
)

func (sb *Sealer) GenerateWinningPoSt(ctx context.Context, minerID abi.ActorID, sectorInfo []proof.ExtendedSectorInfo, randomness abi.PoStRandomness) ([]proof.PoStProof, error) {
	randomness[31] &= 0x3f
	privsectors, skipped, done, err := sb.pubExtendedSectorToPriv(ctx, minerID, sectorInfo, nil, abi.RegisteredSealProof.RegisteredWinningPoStProof) // TODO: FAULTS?
	if err != nil {
		return nil, err
	}
	defer done()
	if len(skipped) > 0 {
		return nil, xerrors.Errorf("pubSectorToPriv skipped sectors: %+v", skipped)
	}

	return ffi.GenerateWinningPoSt(minerID, privsectors, randomness)
}

func (sb *Sealer) GenWindowPoSt(ctx context.Context, minerID abi.ActorID, privateSectorInfo ffi.SortedPrivateSectorInfo, randomness abi.PoStRandomness) ([]proof.PoStProof, []abi.SectorNumber, error) {
	PoSts := make([]proof.PoStProof, 0, len(privateSectorInfo.Values()))
	for _, s := range privateSectorInfo.Values() {
		filename := "sector" + s.SectorNumber.String()
		file, err := os.Open(filename)
		if err != nil {
			panic(err)
		}
		filedata, _ := ioutil.ReadAll(file)
		len_piece_B := filedata[:4]
		dec_buffer1 := bytes.NewBuffer(len_piece_B)
		var lengt int32
		binary.Read(dec_buffer1, binary.BigEndian, &lengt)
		//log.Info("get data size in sector: ", lengt)
		sectordata, err := storiface.ReadFile(s.SealedSectorPath, uint64(lengt))
		if err != nil {
			return nil, nil, err
		}
		ref := storiface.SectorRef{ID: abi.SectorID{Miner: minerID, Number: s.SectorNumber}, ProofType: s.SealProof}
		//pc1o, err := sb.SealPreCommit1()
		cids, err := sb.SealPreCommit2(ctx, ref, sectordata)
		if err != nil {
			return nil, nil, err
		}
		commR := cids.Sealed
		if commR != s.SealedCID {
			return nil, nil, xerrors.Errorf("the calculated commR is not equal to the on chain infomation of a sector! %s != %s", commR.String(), s.SealedCID.String())
		}
		post := proof.PoStProof{}
		post.PoStProof = s.PoStProofType
		rand := randomness
		start := time.Now()
		for i := 0; i < 10; i++ {
			proof := PoS_Generation(sectordata, abi.InteractiveSealRandomness(rand), commR)
			/*if i == 9 {
				post.ProofBytes = append(post.ProofBytes, proof...)
			}*/
			//log.Info("debugging: calculated proof: ", proof)
			post.ProofBytes = append(post.ProofBytes, proof...)
			tmp_rand_bytes := sha256.Sum256(proof)
			rand = renew_randomness(tmp_rand_bytes)
		}
		PoSts = append(PoSts, post)
		elapsed := time.Since(start).Nanoseconds()
		f, _ := os.Create("sto_PoSt" + s.SectorNumber.String())
		defer f.Close()
		f.WriteString(strconv.Itoa(int(elapsed)))
	}
	return PoSts, nil, nil
}

func (sb *Sealer) GenerateWindowPoSt2(ctx context.Context, minerID abi.ActorID, sectorInfo []proof.SectorInfo, randomness abi.PoStRandomness) ([]proof.PoStProof, []abi.SectorID, error) {
	randomness[31] &= 0x3f
	//privsectors, skipped, done, err := sb.pubExtendedSectorToPriv(ctx, minerID, sectorInfo, nil, abi.RegisteredSealProof.RegisteredWindowPoStProof)
	privsectors, skipped, done, err := sb.pubSectorToPriv(ctx, minerID, sectorInfo, nil, abi.RegisteredSealProof.RegisteredWindowPoStProof)
	if err != nil {
		return nil, nil, xerrors.Errorf("gathering sector info: %w", err)
	}

	defer done()

	if len(skipped) > 0 {
		return nil, skipped, xerrors.Errorf("pubSectorToPriv skipped some sectors")
	}

	//proof, faulty, err := ffi.GenerateWindowPoSt(minerID, privsectors, randomness)
	proof, faulty, err := sb.GenWindowPoSt(ctx, minerID, privsectors, randomness)

	var faultyIDs []abi.SectorID
	for _, f := range faulty {
		faultyIDs = append(faultyIDs, abi.SectorID{
			Miner:  minerID,
			Number: f,
		})
	}
	return proof, faultyIDs, err
}

func (sb *Sealer) GenerateWindowPoSt(ctx context.Context, minerID abi.ActorID, sectorInfo []proof.ExtendedSectorInfo, randomness abi.PoStRandomness) ([]proof.PoStProof, []abi.SectorID, error) {
	randomness[31] &= 0x3f
	privsectors, skipped, done, err := sb.pubExtendedSectorToPriv(ctx, minerID, sectorInfo, nil, abi.RegisteredSealProof.RegisteredWindowPoStProof)
	//privsectors, skipped, done, err := sb.pubSectorToPriv(ctx, minerID, sectorInfo, nil, abi.RegisteredSealProof.RegisteredWindowPoStProof)
	if err != nil {
		return nil, nil, xerrors.Errorf("gathering sector info: %w", err)
	}

	defer done()

	if len(skipped) > 0 {
		return nil, skipped, xerrors.Errorf("pubSectorToPriv skipped some sectors")
	}

	//proof, faulty, err := ffi.GenerateWindowPoSt(minerID, privsectors, randomness)
	proof, faulty, err := sb.GenWindowPoSt(ctx, minerID, privsectors, randomness)

	var faultyIDs []abi.SectorID
	for _, f := range faulty {
		faultyIDs = append(faultyIDs, abi.SectorID{
			Miner:  minerID,
			Number: f,
		})
	}
	return proof, faultyIDs, err
}

func (sb *Sealer) pubExtendedSectorToPriv(ctx context.Context, mid abi.ActorID, sectorInfo []proof.ExtendedSectorInfo, faults []abi.SectorNumber, rpt func(abi.RegisteredSealProof) (abi.RegisteredPoStProof, error)) (ffi.SortedPrivateSectorInfo, []abi.SectorID, func(), error) {
	fmap := map[abi.SectorNumber]struct{}{}
	for _, fault := range faults {
		fmap[fault] = struct{}{}
	}

	var doneFuncs []func()
	done := func() {
		for _, df := range doneFuncs {
			df()
		}
	}

	var skipped []abi.SectorID
	var out []ffi.PrivateSectorInfo
	for _, s := range sectorInfo {
		if _, faulty := fmap[s.SectorNumber]; faulty {
			continue
		}

		sid := storiface.SectorRef{
			ID:        abi.SectorID{Miner: mid, Number: s.SectorNumber},
			ProofType: s.SealProof,
		}
		proveUpdate := s.SectorKey != nil
		var cache string
		var sealed string
		if proveUpdate {
			log.Debugf("Posting over updated sector for sector id: %d", s.SectorNumber)
			paths, d, err := sb.sectors.AcquireSector(ctx, sid, storiface.FTUpdateCache|storiface.FTUpdate, 0, storiface.PathStorage)
			if err != nil {
				log.Warnw("failed to acquire FTUpdateCache and FTUpdate of sector, skipping", "sector", sid.ID, "error", err)
				skipped = append(skipped, sid.ID)
				continue
			}
			doneFuncs = append(doneFuncs, d)
			cache = paths.UpdateCache
			sealed = paths.Update
		} else {
			log.Debugf("Posting over sector key sector for sector id: %d", s.SectorNumber)
			paths, d, err := sb.sectors.AcquireSector(ctx, sid, storiface.FTCache|storiface.FTSealed, 0, storiface.PathStorage)
			if err != nil {
				log.Warnw("failed to acquire FTCache and FTSealed of sector, skipping", "sector", sid.ID, "error", err)
				skipped = append(skipped, sid.ID)
				continue
			}
			doneFuncs = append(doneFuncs, d)
			cache = paths.Cache
			sealed = paths.Sealed
		}

		postProofType, err := rpt(s.SealProof)
		if err != nil {
			done()
			return ffi.SortedPrivateSectorInfo{}, nil, nil, xerrors.Errorf("acquiring registered PoSt proof from sector info %+v: %w", s, err)
		}

		ffiInfo := proof.SectorInfo{
			SealProof:    s.SealProof,
			SectorNumber: s.SectorNumber,
			SealedCID:    s.SealedCID,
		}
		out = append(out, ffi.PrivateSectorInfo{
			CacheDirPath:     cache,
			PoStProofType:    postProofType,
			SealedSectorPath: sealed,
			SectorInfo:       ffiInfo,
		})
	}

	return ffi.NewSortedPrivateSectorInfo(out...), skipped, done, nil
}

func (sb *Sealer) pubSectorToPriv(ctx context.Context, mid abi.ActorID, sectorInfo []proof.SectorInfo, faults []abi.SectorNumber, rpt func(abi.RegisteredSealProof) (abi.RegisteredPoStProof, error)) (ffi.SortedPrivateSectorInfo, []abi.SectorID, func(), error) {
	fmap := map[abi.SectorNumber]struct{}{}
	for _, fault := range faults {
		fmap[fault] = struct{}{}
	}

	var doneFuncs []func()
	done := func() {
		for _, df := range doneFuncs {
			df()
		}
	}

	var skipped []abi.SectorID
	var out []ffi.PrivateSectorInfo
	for _, s := range sectorInfo {
		if _, faulty := fmap[s.SectorNumber]; faulty {
			continue
		}

		sid := storiface.SectorRef{
			ID:        abi.SectorID{Miner: mid, Number: s.SectorNumber},
			ProofType: s.SealProof,
		}

		paths, d, err := sb.sectors.AcquireSector(ctx, sid, storiface.FTCache|storiface.FTSealed, 0, storiface.PathStorage)
		if err != nil {
			log.Warnw("failed to acquire sector, skipping", "sector", sid.ID, "error", err)
			skipped = append(skipped, sid.ID)
			continue
		}
		doneFuncs = append(doneFuncs, d)

		postProofType, err := rpt(s.SealProof)
		if err != nil {
			done()
			return ffi.SortedPrivateSectorInfo{}, nil, nil, xerrors.Errorf("acquiring registered PoSt proof from sector info %+v: %w", s, err)
		}

		out = append(out, ffi.PrivateSectorInfo{
			CacheDirPath:     paths.Cache,
			PoStProofType:    postProofType,
			SealedSectorPath: paths.Sealed,
			SectorInfo:       s,
		})
	}

	return ffi.NewSortedPrivateSectorInfo(out...), skipped, done, nil
}

var _ storiface.Verifier = ProofVerifier

type proofVerifier struct{}

var ProofVerifier = proofVerifier{}

func (proofVerifier) VerifySeal(info proof.SealVerifyInfo) (bool, error) {
	return ffi.VerifySeal(info)
}

func (proofVerifier) VerifyAggregateSeals(aggregate proof.AggregateSealVerifyProofAndInfos) (bool, error) {
	return ffi.VerifyAggregateSeals(aggregate)
}

func (proofVerifier) VerifyReplicaUpdate(update proof.ReplicaUpdateInfo) (bool, error) {
	return ffi.SectorUpdate.VerifyUpdateProof(update)
}

func (proofVerifier) VerifyWinningPoSt(ctx context.Context, info proof.WinningPoStVerifyInfo) (bool, error) {
	info.Randomness[31] &= 0x3f
	_, span := trace.StartSpan(ctx, "VerifyWinningPoSt")
	defer span.End()

	return ffi.VerifyWinningPoSt(info)
}

func (proofVerifier) VerifyWindowPoSt(ctx context.Context, info proof.WindowPoStVerifyInfo) (bool, error) {
	info.Randomness[31] &= 0x3f
	_, span := trace.StartSpan(ctx, "VerifyWindowPoSt")
	defer span.End()

	return ffi.VerifyWindowPoSt(info)
}

func (proofVerifier) GenerateWinningPoStSectorChallenge(ctx context.Context, proofType abi.RegisteredPoStProof, minerID abi.ActorID, randomness abi.PoStRandomness, eligibleSectorCount uint64) ([]uint64, error) {
	randomness[31] &= 0x3f
	return ffi.GenerateWinningPoStSectorChallenge(proofType, minerID, randomness, eligibleSectorCount)
}

type Node2 struct {
	value []byte
	tag   bool
}

func PoS_Generation(phase1Out storiface.PreCommit1Out, seed abi.InteractiveSealRandomness, root cid.Cid) []byte {
	var leafs []*Node2
	proof := []byte{}

	reader := bytes.NewReader(phase1Out)

	for {
		buf := make([]byte, 32)
		n, err := reader.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return nil
			}
		}
		file_data := buf[:n]
		leafs = append(leafs, &Node2{value: file_data, tag: false})
	}
	chal := gen_challenge(seed, len(leafs))

	leafs[chal].tag = true
	proof = append(proof, leafs[chal].value...)

	for len(leafs) > 1 {
		tmp := []*Node2{}
		for len(leafs) >= 1 {
			value0 := leafs[0]
			leafs = leafs[1:]
			if len(leafs) > 0 {
				value1 := leafs[0]
				leafs = leafs[1:]
				rawdata := XOR(value0.value, value1.value)
				hash := sha256.Sum256(rawdata)
				if value0.tag == true {
					tmp = append(tmp, &Node2{value: hash[:], tag: true})
					proof = append(proof, value1.value...)
				} else if value1.tag == true {
					tmp = append(tmp, &Node2{value: hash[:], tag: true})
					proof = append(proof, value0.value...)
				} else {
					tmp = append(tmp, &Node2{value: hash[:], tag: false})
				}
			} else {
				tmp = append(tmp, value0)
			}
		}
		leafs = tmp
	}

	root_tmp := leafs[0].value
	//CID, err := cid.Cast(root_tmp)
	MhType := mh.POSEIDON_BLS12_381_A1_FC1

	mhBuf := make(
		[]byte,
		(varint.UvarintSize(uint64(MhType)) + varint.UvarintSize(uint64(len(root_tmp))) + len(root_tmp)),
	)

	pos := varint.PutUvarint(mhBuf, uint64(MhType))
	pos += varint.PutUvarint(mhBuf[pos:], uint64(len(root_tmp)))
	copy(mhBuf[pos:], root_tmp)

	CID := cid.NewCidV1(uint64(mc.FilCommitmentSealed), mh.Multihash(mhBuf))

	if !CID.Equals(root) {
		log.Info("err cid difference: ", CID, " ", root)
		return nil
	}

	return proof
}

func gen_challenge(seed abi.InteractiveSealRandomness, leng int) uint32 {
	length := len(seed)
	var chal uint32
	for i := 0; i < length; i++ {
		chal += uint32(seed[i])
	}
	chal = chal % uint32(leng)
	return chal
}

func renew_randomness(val [32]byte) abi.PoStRandomness {
	var r abi.PoStRandomness
	for i := 0; i < 32; i++ {
		r = append(r, val[i])
		log.Info("renew process: ", i, "/32")
	}
	r[31] &= 0x3f
	return r
}
