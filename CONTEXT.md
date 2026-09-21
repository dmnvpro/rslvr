# rslvr

A local tool that grabs a single frame from a video device, sends it to a vision
model, and shows the model's answer to a quiz question — via CLI or a local web
page.

## Language

**Device**:
A video input visible to the OS — webcam, capture card, or virtual camera. The app never talks to streaming software directly; a virtual camera already presents itself as a Device.
_Avoid_: camera, source, stream

**Frame**:
A single still image (JPEG/PNG) captured from a Device. The UI calls it a "screenshot"; it is always a device frame, never a screen capture.
_Avoid_: screenshot, snapshot, photo

**Shot**:
A Frame saved to disk for later inspection. Saving is optional per request.
_Avoid_: capture, dump

**Job**:
One full cycle: grab a Frame from a Device, optionally save it as a Shot, then Analyze it. Jobs run in a fixed-size pool; when no slot is free a new Job is rejected, never queued.
_Avoid_: task, request

**Answer**:
The model's plain-text answer to the quiz question on the Frame — the letter(s) of the correct option and its text, per the prompt contract.
_Avoid_: response, output, reply

**Result**:
An Answer plus its metadata: raw model reply, model id, latency, token usage, Frame hash. The unit returned to both CLI and web UI.
_Avoid_: answer, payload

**Unreadable**:
The model's verdict that the Frame contains no readable question (canonical reply `UNREADABLE`). Surfaced as a distinct error, never as an empty Answer.
_Avoid_: blank, no-text

**Prompt override**:
A per-request replacement of the configured user prompt. The system prompt is never overridden by a request.
_Avoid_: custom prompt

**Cached Result**:
A Result reused when an identical Frame (same SHA-256) is submitted again within the cache TTL, so tokens are not spent twice.
_Avoid_: history
