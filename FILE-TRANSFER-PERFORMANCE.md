# File transfer throughput candidate — 2026-11-18

This candidate changes native App file transfer and clipboard copy/paste. Both Apps must be updated. There is no fallback to the old clipboard request/reply protocol. The initial candidate preserved formal v0.12.5 artifacts. On user request, this implementation is now included in the refreshed v0.12.5 prerelease assets; both Apps must be downloaded again even if the displayed version is already 0.12.5.

## Changes

- Manual Send Files no longer sleeps for each 16 KiB chunk (the old fixed limit was 1 MiB/s). Reliable ordered SCTP remains responsible for congestion control and retransmission.
- Clipboard files use binary data messages instead of JSON/base64 replies per 8 KiB request. A 256 KiB initial range grows to 8 MiB, with one prefetched successor. At most two receive windows (16 MiB total) and three source jobs are retained per peer. Files are opened once per range; identity, size/modification and permission checks remain.
- A separate reliable `filemeta` channel carries range requests, cancellation and acknowledgements so a full file data queue does not itself hold up the application control messages. SCTP/DTLS and the physical network are still shared.
- SCTP admission is bounded at 8 MiB on direct connections. On NanoDesk Relay, the ceiling is additionally limited to a quarter second of the configured session budget (minimum 64 KiB). Admission waits on buffered-low notifications, with a 5 ms fallback.
- New file admission waits during video-frame writes, control backlog and short video-loss recovery holds. The shared UDP mux and relay client prefer media/RTCP/handshake packets over DTLS application data; an entire outgoing video frame reserves the local media gate.
- NanoDesk Relay routes keep their existing 32-packet / 256-packet global bounds. Media moves ahead of queued bulk packets and can evict a queued bulk packet when the route is full. SCTP recovers discarded file packets. Session/global rate limits remain intact.
- Explorer IStream reads run in an MTA instead of the App's STA UI thread. Each stream has serialized position access and atomic COM references; clones retain the apartment lifetime. Read/CopyTo use 64 KiB chunks and accept valid partial reads.
- Existing authorization, selected-file boundaries, receive limits, temporary files and atomic commit are preserved. Cancellation, offer changes and peer closure stop bounded work. Trace events contain sizes/timings, not file contents or paths.

## Local evidence

Measurements below use race-enabled tests and are not predictions of office throughput.

| Test | Result |
|---|---|
| Clipboard, simulated 50 ms RTT, 36,400,000 bytes | Latest run 2.042 s, **17.00 MiB/s**, exact content match; earlier unloaded run 42.68 MiB/s |
| Old clipboard request/reply bound at 50 ms RTT | Calculated 8 KiB / 50 ms = **0.156 MiB/s**, excluding other overhead; not a measured old-version benchmark |
| Manual Send Files, simulated 50 ms RTT, 23,068,672 bytes | 1.484 s, **14.82 MiB/s**, exact content match; old timer limit 1 MiB/s |
| Clipboard over actual localhost ICE direct / TURN UDP / TURN TLS | 2,000,000 bytes matched on each route |
| Clipboard over NanoDesk UDP / TLS Relay | 2,000,000 bytes matched, approximately 4.20 / 4.30 s under the existing 5 Mbps session budget |
| UI STA makes no progress for 100 ms during another-apartment Read | Read completes successfully without STA message pumping |
| Priorities and bounds | Queued media overtakes bulk; whole-frame reservation holds; relay media evicts bulk within unchanged bounds |
| Cancellation/revocation | Waiting bulk cancels; receiver revocation clears prefetch and source jobs; authorized real file pipeline verifies cancellation cleanup |

Test sources: app/engine/file_performance_test.go, app/native/clipboard_files_test.go, core/traffic/priority_test.go, core/relay/scheduler_test.go, server/native_integration_test.go and server/desktop_integration_test.go. Reproduction logs are under ignored `nanodesk-diagnostics/file-transfer-*.log`.

The old cancellation integration test waited for a progress notification before cancelling a 4 MiB file. At the new speed this could occur after commit. It now cancels explicitly at batch startup; a separate bulk test cancels while transmission admission is blocked. The receiver still verifies incomplete data cannot be committed.

## Limits and deployment

- Priority cannot interrupt an OS/TLS write already in progress or undo packets buffered in a network device. TLS still has TCP head-of-line blocking. External TURN services do not use NanoDesk's server queue scheduler. Concurrent real video plus large-file transfer still needs on-site validation, especially with the unstable USB NIC.
- These changes optimize the native App paths, including clipboard transfers forwarded through another native clipboard source. The Web viewer uses its separate HTTP upload path; it has no SCTP/clipboard file implementation to migrate here.
- Physical link capacity, RTT, loss, disk/antivirus, relay/session policy and competing traffic remain throughput limits. There is no promise of a particular speed or loss-free video on arbitrary links.
- Both Apps need the candidate App EXE. Deploying the candidate Server adds server-side media scheduling; client improvements do not depend on a signaling-format change. No server restart or remote connection was performed. Publication into the existing v0.12.5 prerelease was separately requested after local validation.
- Candidate files: `nanodesk-diagnostics/NanoDesk-App-FastTransfer.exe` and `NanoDesk-Server-FastTransfer.exe` in the same directory. The release refresh rebuilds the formal executables; release hashes are recorded in BUILD-SIZES.txt.

## Validation status

Focused race suites for Engine, native clipboard, traffic and Relay passed. Focused vet for these changed packages passed. The full race run retained the pre-existing capture failure `TestGDIRepeatedInitReleasesHandles: BitBlt invalid handle`; it was not skipped or relaxed. The cancellation test issue found in that run was corrected and its authorized pipeline rerun passed. The final complete server race suite passed in 104.302 s (`file-transfer-server-final.log`).

Candidate SHA256:

- App: `0F4019DD7D6E2B71C0B1268F65B5DCF2D683BBB05E043AD122E51AB0FB872165` (11,933,184 bytes)
- Server: `61565583C9FA00AB950C4449AE945BD9C2CC0B046474B83C2533461FF7F87CBF` (8,322,048 bytes)
