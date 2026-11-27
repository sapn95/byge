#!/usr/bin/env node
// The command. Everything else is in lib.js, which the tests import without
// starting a server on their own stdio.
import { main } from './lib.js';

main();
