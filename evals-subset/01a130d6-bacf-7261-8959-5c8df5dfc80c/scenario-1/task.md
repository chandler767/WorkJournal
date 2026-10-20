# WAV files from recorded audio crash the diarizer

## Problem

The speaker diarizer is broken for audio recorded through the normal microphone workflow. When the pipeline converts a recorded `.m4a` chunk to WAV and then runs diarization on it, the diarizer fails to read the WAV file and errors out. Recordings processed from plain WAV sources (not from the recorder's `.m4a` files) work fine.

The issue traces back to the audio conversion step. `afconvert` — the tool we use to convert `.m4a` to WAV — writes a WAV header format that the diarizer's underlying library doesn't accept. The WAV files it produces from AAC input contain a non-standard extensible header and extra metadata chunks, whereas the diarizer only accepts a plain PCM WAV.

## Expected Behavior

After conversion, WAV files produced from `.m4a` recordings should be readable by the speaker diarizer, the same as WAV files produced from any other source. The audio content (samples) must be preserved exactly — the fix should only affect the header/metadata, not the audio data.

If the converted WAV already has an acceptable header, the fix should leave it untouched.

## Acceptance Criteria

- The pipeline successfully diarizes audio recorded as `.m4a` and converted to WAV, without requiring manual intervention.
- The WAV files written during conversion have a standard PCM header that downstream audio tools can read.
- If the input audio is not 16 kHz mono 16-bit PCM, conversion returns an error rather than silently producing unusable output.
- The original file is left untouched if any error occurs during header normalization.

## When you're done

Stage your changes with `git add`. Then write the proposed commit message for the staged changes to `COMMIT_MSG.txt` at the repository root (do not stage that file). Do not run `git commit`.
