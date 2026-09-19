World Is Agent -- Runtime
=========================

A game-native agent runtime. It runs an LLM-driven agent for game characters:
the game reports what happened, the Runtime decides what the character does, and
the adapter carries that decision back into the game.

This package is the Runtime only. It contains everything the Runtime needs and
nothing else: no installer, no source tree, no Node.js, no Go.


Run it
------

    wia-runtime.exe

That is the whole procedure. The Runtime:

  * creates its data root at %LOCALAPPDATA%\WorldIsAgent on first start
  * writes the shipped agent configuration and game definitions there
  * opens your browser and hands it a session
  * asks for a model provider and an API key on the first run

Nothing is installed system-wide and no administrator rights are needed. To
remove it, delete the folder you extracted and %LOCALAPPDATA%\WorldIsAgent.

Useful options:

    wia-runtime.exe --data-root <dir>   keep the data somewhere else
    wia-runtime.exe --http-addr <addr>  local client address (loopback only)
    wia-runtime.exe --grpc-addr <addr>  adapter address (default 127.0.0.1:50051)
    wia-runtime.exe --no-open           do not open the browser; print the URL

The adapter address is fixed at 127.0.0.1:50051 by default because installed
adapters are configured for it. Change it only if that port is taken; the address
the console reports follows the flag.

The URL printed with --no-open carries a one-time session token in its fragment.
Open it as printed: the token is what lets that browser talk to the Runtime.


What you need
-------------

  * Windows x64
  * A model provider API key (DeepSeek and OpenAI are supported)

The Runtime checks the key with the provider before it saves anything, so a
wrong key is reported on the page and nothing is written to disk.


Connecting the game
-------------------

The Runtime alone does not touch the game. Games are connected by an adapter
that is installed separately, as an SMAPI mod, and is not part of this package.

For Stardew Valley:

  1. Install SMAPI (https://smapi.io) if you have not already.
  2. Get the adapter from the World Is Agent source repository, under
     adapters/stardew, and install its build output as a mod folder so that
     Mods\GameAgentStardew\manifest.json exists. The adapter is built from
     source against your own game installation; there is no prebuilt mod to
     download yet.
  3. Start the Runtime first, then start the game through SMAPI.

The adapter connects to 127.0.0.1:50051, which is where the Runtime listens. Its
config.json is created inside the mod folder on first run; the default address is
already correct, so no edit is normally needed.

Until an adapter is connected, the Runtime works but has nothing to show: the
console lists agent turns, and turns come from the game.


Where your data lives
---------------------

    %LOCALAPPDATA%\WorldIsAgent\config\    agent configuration and model.json
    %LOCALAPPDATA%\WorldIsAgent\secrets\   the API key, as its own file
    %LOCALAPPDATA%\WorldIsAgent\data\      turn traces (traces.jsonl)

The API key is stored in the secrets folder and referenced by path from
model.json, so the configuration never contains it. There is no OS keychain
integration yet: the key is a file on disk, protected by that folder's
permissions. On Windows those permissions are inherited from %LOCALAPPDATA%
rather than set by the Runtime, so the key is readable by your user account,
by administrators, and by anything running with your rights.

Only one Runtime may use a data root at a time. A second instance reports
store_in_use and exits.


Status and limits
-----------------

This is an experimental 0.1.0 build. The console shows runtime state and a
summary of recent agent turns; it does not yet offer per-turn detail, task or
memory views, or dependency diagnostics. See docs/STATUS.md in the repository
for the current validation scope and known limits.


License
-------

MIT. See LICENSE.
