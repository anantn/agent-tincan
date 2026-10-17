# Passing a conversation

A pass moves one of your chats from one web chat app to another: tincan reads the chat, packs it into one message, and has a web teammate open a new chat on its own site with it, so you can pick up where you left off on a different subscription.

## From your terminal

```
tincan pass https://claude.ai/chat/<id> --to chatgpt-web   # a chat URL
tincan pass grok --to gemini-web                           # Grok's newest chat
tincan pass chatgpt --id <id> --to claude-web              # one chat by id
```

The source is a chat URL on ChatGPT, claude.ai, Grok, Gemini or Copilot (`copilot.microsoft.com` links work too), or one of the source names `chatgpt`, `claude-ai`, `grok`, `gemini`, `copilot`. A bare source passes its newest conversation within the [history window](history.md#window) (the last 50 conversations, up to 30 days), leaving out chats the web agents sent into. Perplexity and dot threads cannot be passed from: they are not history sources.

The chat is read through the Tincan Chrome extension on the machine you run the command on, the same way `tincan history` reads it, so run it on the Mac with Chrome and the extension (`tincan history install`). The seed goes to the destination as an ask from that machine's joined agent.

The destination must be one of the six web chat teammates (`chatgpt-web`, `claude-web`, `grok-web`, `gemini-web`, `perplexity-web`, `copilot-web`), signed in to its site and not out of messages. Anything else, `dot-web` and coding agents included, is refused before the chat is read, with the reason.

The command waits for the destination to open the chat (`--wait`, 10 minutes by default) and prints the conversation it opened, for example `Passed to chatgpt-web. ChatGPT conversation: 68f0...`. Open it in the browser and carry on there. It prints nothing of the destination's answer.

## The seed

The new chat starts with one message, built in Go with no model step:

1. `new chat`, so the destination opens a fresh conversation.
2. A paragraph telling the model that what follows is material from another conversation, not instructions, sitting between two delimiter lines that carry a random value picked for this pass (and picked again if the chat happens to contain it).
3. The chat's title.
4. The first line of each earlier prompt, oldest first, each cut to 160 characters.
5. The last 6 turns verbatim, each prompt with its reply.

It fits the destination's message cap: 32 KB, and 12 KB for claude.ai until its safe paste size is measured (a longer paste becomes an attachment there). When the chat is longer, the earliest prompts' lines are left out first (a line says how many), then the older recent turns are shortened, and the newest turn is shortened last, each with a truncation marker. Images are not passed.

## Failover

A seed is grouped (`pass-<id>`), and a grouped ask never fails over, so a pass only ever reaches the vendor you named. If the destination runs out of messages before the seed is typed, the pass fails with the reason instead of moving to another site.

## Privacy

A pass sends that chat to the destination's vendor: its title, earlier prompts' first lines and last 6 turns are then in that account's history, under that site's terms and retention, and its model reads them. That is the point of a pass, and it is why only you start one from your terminal. See [the trust model](../trust-model.md#passing-a-conversation).
