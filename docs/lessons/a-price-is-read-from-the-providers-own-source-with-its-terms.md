---
title: "A price is read from the provider's own source, with its terms"
description: "A summarised VPS price that hid its term and a mandatory option, the payment approval given on it, and where a price and its stock are read instead."
type: lesson
topics: [provisioning]
evidence: ["../plans/providers-the-platform-adapts-to-more-than-one.md"]
---

On 2026-11-16 the first comparison of providers listed an OVHcloud VPS with
8 GB at 7.21 EUR a month, taken from a web page that summarised OVHcloud's
offer. The operator approved a one-month trial on that figure. OVHcloud's own
catalog API (`/1.0/order/catalog/public/vps`, no login) then gave different
numbers. The cheapest 8 GB plan for one month without commitment was VPS-1 of
the 2026 range at 6.49 EUR, plus a backup option the catalog marks as
mandatory, 0.50 EUR. Read again on 2026-11-17, the catalog showed where 7.21
came from: it is the price of VPS-2 of the 2027 range when twelve months are
paid upfront, a plan that costs 8.49 EUR a month. The summary carried neither
the term nor the mandatory option, and it said nothing about stock. The same
API's stock endpoint (`/1.0/vps/order/rule/datacenter`) showed that day that no
8 GB plan could be ordered at any OVHcloud site. The approval was given again
on the catalog's figures before anything was ordered, so no money was lost,
but a payment decision had rested on a figure that matched no order a
one-month trial could place.

**A price or a stock figure is read from the provider's own API or the live
system, with its terms (term length, mandatory options, tax), and the reading
names its source and time.** A summary page drops exactly the terms that make
two prices comparable, and it can be old without saying so. The trial that
came out of this is part C of the
[Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md),
and its order still reads the price again at order time.
