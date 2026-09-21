# rslvr

A local tool that grabs a single frame from a video device, sends it to a vision
model, and shows the model's answer to a quiz question — via CLI or a local web
page.

## Language

**Device**:
A video input visible to the OS — webcam, capture card, or virtual camera. The app never talks to streaming software directly; a virtual camera already presents itself as a Device.
_Avoid_: camera, source, stream

**Frame**:
A single still image, always captured losslessly (PNG) from a Device — quiz text must survive capture without compression artifacts. The UI calls it a "screenshot"; it is always a device frame, never a screen capture.
_Avoid_: screenshot, snapshot, photo

**Frame set**:
The ordered set of Frames captured for one question and analyzed together — the unit the model reads as "Frame 1..N". A single-frame set is the normal case; more frames cover a question longer than one screen. Frames are never reordered after capture.
_Avoid_: batch, gallery, collage

**Shot**:
A Frame saved to disk for later inspection. Saving is optional per request.
_Avoid_: capture, dump

**Job**:
One unit of work on the fixed-size pool — either grabbing a Frame from a Device (optionally saving it as a Shot) or analyzing a Frame set into a Result. When no slot is free a new Job is rejected, never queued.
_Avoid_: task, request

**Answer**:
The model's plain-text answer to the quiz question on the Frame — the letter(s) of the correct option and its text; for an open question with no options, the free-form answer text without letters. Per the prompt contract.
_Avoid_: response, output, reply

**Explanation**:
The model's short justification of why the Answer is correct, returned alongside it and shown on demand. Never part of the Answer itself.
_Avoid_: reasoning, rationale, comment

**Result**:
An Answer plus its metadata: raw model reply, model id, latency, token usage, Frame hash. The unit returned to both CLI and web UI.
_Avoid_: answer, payload

**Unreadable**:
The model's verdict that the Frame contains no readable question (canonical reply `UNREADABLE`). Surfaced as a distinct error, never as an empty Answer.
_Avoid_: blank, no-text

**Prompt override**:
A per-request replacement of the configured user prompt. The system prompt is never overridden by a request.
_Avoid_: custom prompt

**History**:
The ordered list of Results from the current web-UI session — most recent first, capped, held only in the browser tab's memory and lost on page reload. Exported to a text file manually from the UI: downloaded to the client or written into the Shot directory on the server. Not a Cached Result: the cache reuses a Result by Frame hash, the History is a session log.
_Avoid_: log, cache, report

**Cached Result**:
A Result reused when an identical Frame (same SHA-256) is submitted again within the cache TTL, so tokens are not spent twice.
_Avoid_: history
