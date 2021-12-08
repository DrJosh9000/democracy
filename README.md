# Democracy

A proof-of-concept Bitcoin client that treats Bitcoin with 
[the contempt it deserves](https://www.stephendiehl.com/blog.html). 

Bitcoin offers no inbuilt democracy. One can either vote to support the network
(by running a regular Bitcoin client or doing Bitcoin mining), or abstain (by
not doing those things). Bitcoin is thus a one-party state.

Running this software is a *vote against Bitcoin*. It operates by either
directly rejecting, or refusing to store or forward, certain Bitcoin messages
to peers in the network. Effectively, this software treats Bitcoins as having
zero or negative value. (Which they do.)

## Usage

After installing [Go](https://go.dev):

```go
go install github.com/DrJosh9000/democracy@latest
```

A binary called `democracy` should now exist in your `~/go/bin`. Run it

## What about other blockchains, cryptocurrencies, NFTs?

If someone wants to take the concept and implement it for an undemocratic
system of their choice, patches are most welcome.

## Is this a hacking tool?

No, don't be silly. It doesn't steal Bitcoins or stop a regular Bitcoin client 
from running. Why would I want to steal something with negative value?

## But this aims to disrupt Bitcoin?

In the sense that every business and shop owner that doesn't accept Bitcoin
as payment is also "disrupting" Bitcoin. 

## This isn't following the Bitcoin protocol.

Actually it does - it sends and receives validly-formed Bitcoin messages at the
correct times. It applies a different *policy* to the messages it chooses to
send.

## How much storage, CPU, memory, and network bandwidth does this consume?

Not very much. Because Bitcoin is treated as valueless, there is no need to
store or verify the (enormous) blockchain, and because no incoming information
is relayed, there is not much outbound traffic. The main resources used are
memory and CPU for tracking peers, and network connections to peers.