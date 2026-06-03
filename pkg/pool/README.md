# pkg/pool

GPU pool manager: maintains a desired instance count across providers (`PoolManager`, `Acquire`/`ReturnToPool`) behind the `GPUProvider` seam. Supplies the leasable GPU capacity that marketplace placement draws on.

See docs/architecture/PHASE_8C_INTEGRATION.md
