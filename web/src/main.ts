import { mount } from 'svelte'
import './styles/fonts'
import './styles/tokens.css'
import './styles/base.css'
import App from './App.svelte'

mount(App, { target: document.getElementById('app')! })
