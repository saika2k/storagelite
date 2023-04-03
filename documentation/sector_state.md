```go
const(	
	UndefinedSectorState SectorState = ""

	// happy path
	Empty      SectorState = "Empty"      // deprecated
	WaitDeals  SectorState = "WaitDeals"  // waiting for more pieces (deals) to be added to the sector
	AddPiece   SectorState = "AddPiece"   // put deal data (and padding if required) into the sector
	Packing    SectorState = "Packing"    // sector not in sealStore, and not on chain
	GetTicket  SectorState = "GetTicket"  // generate ticket
	PreCommit1 SectorState = "PreCommit1" // do PreCommit1
	PreCommit2 SectorState = "PreCommit2" // do PreCommit2

	PreCommitting SectorState = "PreCommitting" // on chain pre-commit
	PreCommitWait SectorState = "PreCommitWait" // waiting for precommit to land on chain

	SubmitPreCommitBatch SectorState = "SubmitPreCommitBatch"
	PreCommitBatchWait   SectorState = "PreCommitBatchWait"

	WaitSeed             SectorState = "WaitSeed"       // waiting for seed
	Committing           SectorState = "Committing"     // compute PoRep
	CommitFinalize       SectorState = "CommitFinalize" // cleanup sector metadata before submitting the proof (early finalize)
	CommitFinalizeFailed SectorState = "CommitFinalizeFailed"

	// single commit
	SubmitCommit SectorState = "SubmitCommit" // send commit message to the chain
	CommitWait   SectorState = "CommitWait"   // wait for the commit message to land on chain

	SubmitCommitAggregate SectorState = "SubmitCommitAggregate"
	CommitAggregateWait   SectorState = "CommitAggregateWait"

	FinalizeSector SectorState = "FinalizeSector"
	Proving        SectorState = "Proving"
	Available      SectorState = "Available" // proving CC available for SnapDeals

	// snap deals / cc update
	SnapDealsWaitDeals    SectorState = "SnapDealsWaitDeals"
	SnapDealsAddPiece     SectorState = "SnapDealsAddPiece"
	SnapDealsPacking      SectorState = "SnapDealsPacking"
	UpdateReplica         SectorState = "UpdateReplica"
	ProveReplicaUpdate    SectorState = "ProveReplicaUpdate"
	SubmitReplicaUpdate   SectorState = "SubmitReplicaUpdate"
	ReplicaUpdateWait     SectorState = "ReplicaUpdateWait"
	FinalizeReplicaUpdate SectorState = "FinalizeReplicaUpdate"
	UpdateActivating      SectorState = "UpdateActivating"
	ReleaseSectorKey      SectorState = "ReleaseSectorKey"

	// external import
	ReceiveSector SectorState = "ReceiveSector"

	// error modes
	FailedUnrecoverable  SectorState = "FailedUnrecoverable"
	AddPieceFailed       SectorState = "AddPieceFailed"
	SealPreCommit1Failed SectorState = "SealPreCommit1Failed"
	SealPreCommit2Failed SectorState = "SealPreCommit2Failed"
	PreCommitFailed      SectorState = "PreCommitFailed"
	ComputeProofFailed   SectorState = "ComputeProofFailed"
	RemoteCommitFailed   SectorState = "RemoteCommitFailed"
	CommitFailed         SectorState = "CommitFailed"
	PackingFailed        SectorState = "PackingFailed" // TODO: deprecated, remove
	FinalizeFailed       SectorState = "FinalizeFailed"
	DealsExpired         SectorState = "DealsExpired"
	RecoverDealIDs       SectorState = "RecoverDealIDs"

	// snap deals error modes
	SnapDealsAddPieceFailed     SectorState = "SnapDealsAddPieceFailed"
	SnapDealsDealsExpired       SectorState = "SnapDealsDealsExpired"
	SnapDealsRecoverDealIDs     SectorState = "SnapDealsRecoverDealIDs"
	AbortUpgrade                SectorState = "AbortUpgrade"
	ReplicaUpdateFailed         SectorState = "ReplicaUpdateFailed"
	ReleaseSectorKeyFailed      SectorState = "ReleaseSectorKeyFailed"
	FinalizeReplicaUpdateFailed SectorState = "FinalizeReplicaUpdateFailed"

	Faulty        SectorState = "Faulty"        // sector is corrupted or gone for some reason
	FaultReported SectorState = "FaultReported" // sector has been declared as a fault on chain
	FaultedFinal  SectorState = "FaultedFinal"  // fault declared on chain

	Terminating       SectorState = "Terminating"
	TerminateWait     SectorState = "TerminateWait"
	TerminateFinality SectorState = "TerminateFinality"
	TerminateFailed   SectorState = "TerminateFailed"

	Removing     SectorState = "Removing"
	RemoveFailed SectorState = "RemoveFailed"
	Removed      SectorState = "Removed"
)
```