#!/usr/bin/env node
import { AGENT_VERSION } from '../index.js';
import { SHARED_VERSION } from '@preview/shared';

console.log(`Hello Agent (agent=${AGENT_VERSION}, shared=${SHARED_VERSION})`);
